-- Outbound voice alerts (M2 Part B Task 10; Build Prompt §10.4 ladder;
-- scenario 9; ADR-0027).
--
-- The ladder: SMS at once (dispatch-worker); a voice call when the alert is
-- still pending after voice_fallback_after (20 s, decision 8), placed by
-- ivr-controller, which owns Asterisk. ivr-controller claims the voice
-- attempt through the same op.begin_attempt (once per alert; never while
-- dispatch is suspended; only for a pending alert), opens the responder's
-- number to dial it, and takes the responder's keypad answer through
-- op.accept_alert / op.decline_alert like the USSD menu.

GRANT EXECUTE ON FUNCTION op.begin_attempt(uuid, text, text), op.finish_attempt(uuid, text, text) TO op_ivr;
GRANT SELECT (id, alert_id, purpose, channel, status) ON op.alert_attempts TO op_ivr;
GRANT SELECT (phone_ct, phone_key_id) ON op.responders TO op_ivr;
