package chronos_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	chronos "chronos.sh/go"
)

func ExampleNewClient() {
	client, err := chronos.NewClient(os.Getenv("CHRONOS_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	account, err := client.Account.Get(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Connected to", account.Name)
}

func ExampleSchedulesService_Create() {
	client, err := chronos.NewClient(os.Getenv("CHRONOS_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	schedule, err := client.Schedules.Create(context.Background(), chronos.ScheduleCreateParams{
		Name:     "Nightly report",
		Cron:     new("0 2 * * *"),
		Timezone: new("Europe/London"),
		Delivery: &chronos.Delivery{
			Type: chronos.DeliveryTypePush,
			HTTP: &chronos.HTTPDelivery{URL: "https://example.com/hooks/report", Method: http.MethodPost},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(schedule.ID, schedule.NextRunAt)
}

func ExampleSchedulesService_Update() {
	client, err := chronos.NewClient(os.Getenv("CHRONOS_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	// Rename the schedule and remove its run limit. Other fields stay as they are.
	_, err = client.Schedules.Update(context.Background(), "SCHEDULE_ID", chronos.ScheduleUpdateParams{
		Name: new("Nightly report (EU)"),
		Runs: chronos.Null[int](),
	})
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleSchedulesService_Pause() {
	client, err := chronos.NewClient(os.Getenv("CHRONOS_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	_, err = client.Schedules.Pause(context.Background(), "SCHEDULE_ID")
	apiErr, ok := errors.AsType[*chronos.APIError](err)
	if ok && apiErr.Code == chronos.ErrorCodeScheduleAlreadyPaused {
		err = nil
	}
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleJobsService_Create() {
	client, err := chronos.NewClient(os.Getenv("CHRONOS_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	job, err := client.Jobs.Create(context.Background(), chronos.JobCreateParams{
		Name:           "Send welcome email",
		Handler:        new("send-email"),
		Delay:          &chronos.Interval{Value: 10, Unit: chronos.IntervalUnitMinute},
		Payload:        map[string]any{"user_id": "usr_123"},
		IdempotencyKey: new("welcome-usr_123"),
	})
	apiErr, ok := errors.AsType[*chronos.APIError](err)
	if ok && apiErr.Code == chronos.ErrorCodeIdempotencyKeyConflict {
		fmt.Println("Already scheduled")
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(job.ID)
}

func ExampleJobsService_Wait() {
	client, err := chronos.NewClient(os.Getenv("CHRONOS_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	job, err := client.Jobs.Wait(ctx, "JOB_ID", chronos.JobWaitOptions{})
	_, timedOut := errors.AsType[*chronos.WaitTimeoutError](err)
	if timedOut {
		log.Fatal("job still not finished")
	}
	if err != nil {
		log.Fatal(err)
	}
	if job.Status == chronos.JobStatusFailed && job.LastError != nil {
		fmt.Println("failed:", *job.LastError)
	}
}

func ExampleJobsService_Iterate() {
	client, err := chronos.NewClient(os.Getenv("CHRONOS_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	// Collect ids first. Retrying while iterating the failed filter moves
	// jobs out of it and shifts later pages.
	var failed []string
	for job, err := range client.Jobs.Iterate(ctx, chronos.JobListParams{Status: chronos.JobStatusFailed}) {
		if err != nil {
			log.Fatal(err)
		}
		failed = append(failed, job.ID)
	}
	for _, id := range failed {
		if _, err := client.Jobs.Retry(ctx, id); err != nil {
			log.Fatal(err)
		}
	}
}

func ExampleWebhook_Verify() {
	webhook, err := chronos.NewWebhook([]string{os.Getenv("CHRONOS_SIGNING_SECRET")})
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/chronos", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		if err := webhook.Verify(body, r.Header); err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		var job chronos.PushedJob
		if err := json.Unmarshal(body, &job); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		var payload struct {
			Report string `json:"report"`
		}
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		log.Printf("job %s, attempt %d: building %s", job.JobID, job.Attempt, payload.Report)
		w.WriteHeader(http.StatusOK)
	})
}
