package process

import "time"

type CaseValue struct {
	Field      string    `json:"field"`
	Value      any       `json:"value"`
	NodeID     string    `json:"nodeId"`
	ActorID    string    `json:"actorId"`
	RecordedAt time.Time `json:"recordedAt"`
}
