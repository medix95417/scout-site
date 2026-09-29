-- Where an enquiry came from.
--
-- Until now every prospect arrived the same way: a family filled in the
-- public form. A leader can now add one by hand — from a council lead
-- list, a school recruiting night sheet, a conversation in a car park —
-- and the difference matters enough to record rather than infer.
--
-- It matters because of email. A form submission gets an automatic
-- reply, because the family just asked and is probably still looking at
-- the screen. Someone off a list asked for nothing, and a "thanks for
-- your enquiry" they don't recognise is at best confusing. Making the
-- distinction a column rather than a convention means the auto-reply
-- can be tied to it structurally, instead of relying on every future
-- caller remembering which kind of prospect it is holding.
--
-- It also answers, later and honestly, "did this person ever contact
-- us?" — which is the question behind any complaint about unsolicited
-- mail, and one nothing else in the row can answer.

ALTER TABLE prospects ADD COLUMN source text NOT NULL DEFAULT 'form'
    CHECK (source IN ('form', 'leader'));

-- Everything already stored arrived through the form; that is the only
-- way there was.
COMMENT ON COLUMN prospects.source IS
    'form = filled in the public join form; leader = entered by a leader on the admin page';
