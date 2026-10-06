package store

import (
	"encoding/json"
	"fmt"
	"time"
)

// jobCreatedPayload snapshots the machine as it was when the job opened and
// records whether the job started from stored coefficients.
type jobCreatedPayload struct {
	Job     Job       `json:"job"`
	Machine Machine   `json:"machine"`
	At      time.Time `json:"at"`
}

type runRecordedPayload struct {
	Input RunInput  `json:"input"`
	At    time.Time `json:"at"`
}

type runCorrectedPayload struct {
	Input CorrectionInput `json:"input"`
	At    time.Time       `json:"at"`
}

func marshalPayload(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // all payloads are plain structs
	}
	return b
}

func decodeEvent(ev *Event) (any, error) {
	switch ev.Type {
	case EvJobCreated:
		var p jobCreatedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return nil, err
		}
		return &p, nil
	case EvRunRecorded:
		var p runRecordedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return nil, err
		}
		return &p, nil
	case EvRunCorrected:
		var p runCorrectedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return nil, err
		}
		return &p, nil
	}
	return nil, fmt.Errorf("unknown event type %q", ev.Type)
}

// EventView is the serializable form of an Event with the raw payload inline.
type EventView struct {
	ID        int64           `json:"id"`
	JobID     string          `json:"job_id"`
	Type      EventType       `json:"type"`
	Seq       int64           `json:"seq"`
	RunTime   time.Time       `json:"run_time"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

// BuildEventViews converts stored events to their serializable form.
func BuildEventViews(evs []*Event) []EventView {
	out := make([]EventView, len(evs))
	for i, ev := range evs {
		out[i] = EventView{
			ID: ev.ID, JobID: ev.JobID, Type: ev.Type, Seq: ev.Seq,
			RunTime: ev.RunTime, CreatedAt: ev.CreatedAt, Payload: json.RawMessage(ev.Payload),
		}
	}
	return out
}
