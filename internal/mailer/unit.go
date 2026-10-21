package mailer

import "context"

// Which unit a message is sent for.
//
// The server, port and login are one install-wide thing, but the From
// address is not: the Troop's mail should come from the Troop and the Pack's
// from the Pack (settings.MailFrom). A send says which it is for by carrying
// the unit on its context, rather than every Send growing a unit parameter
// that most callers would have to thread through from somewhere far away.
//
// A send with no unit on its context behaves exactly as it did before units
// had their own address: the site-wide From.
//
// Whoever knows the unit attaches it, as close to the send as possible — a
// newsletter attaches its own unit's id inside the sender, not its caller —
// and TestEverySendNamesItsUnit fails a send that does not, because the
// failure is otherwise silent: it still sends, from the wrong address.

type unitKey struct{}

// WithUnit returns ctx marked as sending for unitID.
func WithUnit(ctx context.Context, unitID string) context.Context {
	if unitID == "" {
		return ctx
	}
	return context.WithValue(ctx, unitKey{}, unitID)
}

// UnitFromContext is the unit a send is for, if its context names one.
func UnitFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(unitKey{}).(string)
	return id, ok && id != ""
}
