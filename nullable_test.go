package chronos

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPageUsesSnakeCaseKeys(t *testing.T) {
	got, err := json.Marshal(Page[int]{Data: []int{1}, Paging: Paging{Page: 1, Pages: 1, Size: 20, Total: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"data":[1],"paging":{"page":1,"pages":1,"size":20,"total":1}}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestUpdateParamsTellOmittedFromNull(t *testing.T) {
	stopAt := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		params ScheduleUpdateParams
		want   string
	}{
		{name: "all omitted", params: ScheduleUpdateParams{}, want: `{}`},
		{name: "null clears", params: ScheduleUpdateParams{Description: Null[string](), Payload: Null[map[string]any]()}, want: `{"description":null,"payload":null}`},
		{
			name:   "values are sent",
			params: ScheduleUpdateParams{Runs: NewNullable(0), StopAt: NewNullable(stopAt), MaxRetries: new(0)},
			want:   `{"runs":0,"stop_at":"2027-01-01T00:00:00Z","max_retries":0}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.params)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}
