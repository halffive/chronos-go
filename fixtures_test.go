package chronos

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestTypesCoverEveryResponseField(t *testing.T) {
	tests := map[string]any{
		"schedule.json":    &Schedule{},
		"job.json":         &Job{},
		"execution.json":   &Execution{},
		"account.json":     &Account{},
		"claimed_job.json": &ClaimedJob{},
	}
	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			dec := json.NewDecoder(bytes.NewReader([]byte(fixture(t, name))))
			dec.DisallowUnknownFields()
			if err := dec.Decode(target); err != nil {
				t.Fatal(err)
			}
		})
	}
}
