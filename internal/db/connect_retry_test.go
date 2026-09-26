package db

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Connect retries a Postgres that isn't up yet, because this process
// and the database start together on every reboot. It must not retry a
// configuration mistake, which will read exactly the same way in thirty
// seconds' time.

func TestWorthRetrying(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			"connection refused — Postgres hasn't bound its port yet",
			&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")},
			true,
		},
		{
			"the container's name doesn't resolve yet",
			&net.DNSError{Err: "server misbehaving", Name: "db"},
			true,
		},
		{
			"Postgres is up but still starting",
			&pgconn.PgError{Code: "57P03", Message: "the database system is starting up"},
			true,
		},
		{
			// A server that answers has answered. Waiting changes nothing.
			"the password is wrong",
			&pgconn.PgError{Code: "28P01", Message: "password authentication failed"},
			false,
		},
		{
			"the database doesn't exist",
			&pgconn.PgError{Code: "3D000", Message: "database \"scoutsite\" does not exist"},
			false,
		},
		{
			"something else entirely",
			errors.New("who knows"),
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := worthRetrying(c.err); got != c.want {
				t.Errorf("worthRetrying(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}

	t.Run("wrapped errors are still recognised", func(t *testing.T) {
		wrapped := errors.Join(errors.New("dialing"), &net.OpError{Op: "dial", Err: errors.New("connection refused")})
		if !worthRetrying(wrapped) {
			t.Error("a wrapped dial error wasn't recognised")
		}
	})
}

// The retry has to actually happen — an unreachable server must be
// tried again rather than failing on the first refusal, which is the
// whole behaviour this exists for.
func TestConnectRetriesAnUnreachableServer(t *testing.T) {
	// A port nothing is listening on: taking a listener's address and
	// closing it, so the port is real and certain to refuse.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	restore := shrinkConnectTimings(t, 400*time.Millisecond, 50*time.Millisecond)
	defer restore()

	start := time.Now()
	pool, err := Connect(t.Context(), "postgres://someone@"+addr+"/nothing?sslmode=disable")
	elapsed := time.Since(start)

	if err == nil {
		pool.Close()
		t.Fatal("connecting to a closed port succeeded")
	}
	// Without the retry this returns in microseconds. With it, it keeps
	// trying until the deadline.
	if elapsed < connectWait {
		t.Errorf("gave up after %s, before the %s deadline — it isn't retrying", elapsed, connectWait)
	}
}

// And it has to stop. A deadline that isn't honoured turns a dead
// database into a container that never reports anything at all.
func TestConnectGivesUpAtTheDeadline(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	restore := shrinkConnectTimings(t, 200*time.Millisecond, 20*time.Millisecond)
	defer restore()

	done := make(chan error, 1)
	go func() {
		pool, err := Connect(t.Context(), "postgres://someone@"+addr+"/nothing?sslmode=disable")
		if pool != nil {
			pool.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a failure")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Connect never returned — the deadline isn't being honoured")
	}
}

// shrinkConnectTimings makes the retry window short enough to test, and
// returns a function restoring the real values.
func shrinkConnectTimings(t *testing.T, wait, backoff time.Duration) func() {
	t.Helper()
	origWait, origFirst, origMax := connectWait, connectFirstBackoff, connectMaxBackoff
	connectWait, connectFirstBackoff, connectMaxBackoff = wait, backoff, backoff
	return func() {
		connectWait, connectFirstBackoff, connectMaxBackoff = origWait, origFirst, origMax
	}
}
