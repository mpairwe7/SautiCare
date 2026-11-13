package intake

import (
	"context"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
)

// IVRCall is the voice channel's per-call state, held by the IVR adapter
// for the life of the call (a voice call is one connection, unlike USSD).
type IVRCall struct {
	CallID     string
	IncidentID uuid.UUID
	HasPhone   bool
	Shown      Kind // the prompt being played; the next input answers it
}

// IVRTurn is what to play next.
type IVRTurn struct {
	Call   IVRCall
	Prompt Prompt
}

func ivrSession(c IVRCall) session { return session{idem: "ivr:" + c.CallID, hasPhone: c.HasPhone} }

// StartIVR opens the incident for a voice call, or resumes the caller's
// unfinished incident from either channel (Guide §4), and returns the first
// prompt. A withheld number is unknown: it never resumes, and the callback
// screen asks for a number (Build Prompt §10.2).
func (s *Service) StartIVR(ctx context.Context, callID string, phone pii.Phone) (IVRTurn, error) {
	call := IVRCall{CallID: callID, HasPhone: !phone.IsZero()}
	opened, err := s.open(ctx, incident.ChannelIVR, callID, "", "", phone, ivrSession(call).idem+":start")
	if err != nil {
		return IVRTurn{}, err
	}
	call.IncidentID = opened.IncidentID
	// A resumed incident may lack what an earlier, failed request on either
	// channel was to record (its classification, T2): record it first.
	p, err := s.catchUp(ctx, call.IncidentID, "")
	if err != nil {
		return IVRTurn{}, err
	}
	pr, err := s.next(ctx, p, call.HasPhone)
	if err != nil {
		return IVRTurn{}, err
	}
	call.Shown = pr.Kind
	return IVRTurn{Call: call, Prompt: pr}, nil
}

// AnswerIVR applies keypad input given to the prompt being played. It is
// the USSD flow with voice semantics: option 6 at screen 7 is "another
// landmark" without free text (located at ward level, call-back flagged).
func (s *Service) AnswerIVR(ctx context.Context, call IVRCall, input string) (IVRTurn, error) {
	sess := ivrSession(call)
	pr, err := s.answer(ctx, call.IncidentID, &sess, call.Shown, input, true)
	if err != nil {
		return IVRTurn{}, err
	}
	call.Shown = pr.Kind
	return IVRTurn{Call: call, Prompt: pr}, nil
}
