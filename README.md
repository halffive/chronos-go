# chronos-go

Go client for [Chronos](https://chronos.sh): recurring schedules and one-off jobs, delivered to your own code.

```go
client, err := chronos.NewClient(os.Getenv("CHRONOS_API_KEY"))
if err != nil {
	log.Fatal(err)
}

schedule, err := client.Schedules.Create(ctx, chronos.ScheduleCreateParams{
	Name: "Nightly report",
	Cron: new("0 2 * * *"),
	Delivery: &chronos.Delivery{
		Type: chronos.DeliveryTypePush,
		HTTP: &chronos.HTTPDelivery{URL: "https://example.com/hooks/report", Method: "POST"},
	},
})
```

Chronos calls your endpoint on every run, and `Webhook.Verify` checks that the call came from Chronos. Requires Go 1.26 or later.

## Install

```sh
go get chronos.sh/go
```

## Quick start

Create an API key in the [Chronos dashboard](https://app.chronos.sh) and export it as `CHRONOS_API_KEY`.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	chronos "chronos.sh/go"
)

func main() {
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
			HTTP: &chronos.HTTPDelivery{URL: "https://example.com/hooks/chronos", Method: "POST"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("created", schedule.ID, "next run", schedule.NextRunAt)
	// created 0192f3a0-7a8b-7000-8000-000000000001 next run 2026-10-26 02:00:00 +0100 BST
}
```

Replace the URL with an endpoint you control. The schedule runs every day at 02:00 London time.

## Usage

The client has one service per resource: `Schedules`, `Jobs`, `Executions` and `Account`. Every method takes a `context.Context` first.

### Optional fields

Optional request fields are pointers. `new(value)` builds one:

```go
schedule, err := client.Schedules.Create(ctx, chronos.ScheduleCreateParams{
	Name:     "Weekly digest",
	Cron:     new("0 9 * * MON"),
	Timezone: new("America/New_York"),
})
```

Update fields that can be cleared use `Nullable`. Fields you leave out stay as they are:

```go
_, err := client.Schedules.Update(ctx, scheduleID, chronos.ScheduleUpdateParams{
	Name: new("Nightly report (EU)"),
	Runs: chronos.Null[int](),        // clear the run limit
})
```

### Lists and iteration

`List` returns one page. `Iterate` walks every page:

```go
for job, err := range client.Jobs.Iterate(ctx, chronos.JobListParams{Status: chronos.JobStatusFailed}) {
	if err != nil {
		return err
	}
	fmt.Println(job.ID, job.Name)
}
```

### Triggering and waiting

Run a schedule on demand with `Trigger`, then block until the job finishes:

```go
job, err := client.Schedules.Trigger(ctx, scheduleID)
```

```go
ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
defer cancel()

job, err := client.Jobs.Wait(ctx, jobID, chronos.JobWaitOptions{})
```

### Errors

A rejected request returns `*chronos.APIError`. Branch on its `Code`:

```go
_, err := client.Schedules.Pause(ctx, scheduleID)
apiErr, ok := errors.AsType[*chronos.APIError](err)
if ok && apiErr.Code == chronos.ErrorCodeScheduleAlreadyPaused {
	err = nil
}
```

`errors.Is(err, chronos.ErrNotFound)` tests the status. `ErrUnauthorized` and `ErrRateLimited` work the same way.

A request that got no response returns `*chronos.NetworkError`. When an attempt times out, it matches both `chronos.ErrTimeout` and `context.DeadlineExceeded`. To tell a Chronos timeout from your own context's deadline, check `ctx.Err()` first. `Jobs.Wait` returns `*chronos.WaitTimeoutError` instead when the job is still running at the deadline.

### Retries and timeouts

Each attempt times out after 30 seconds. Change it with `chronos.WithRequestTimeout`. A deadline on your context bounds all attempts.

Any request rejected with 429 is retried, because the API rejects it before doing any work. GET requests are also retried after a network error, a timeout or a 5xx. Writes are not retried in those cases, so a lost response cannot create a schedule twice. Change the retry count with `chronos.WithMaxRequestRetries`.

### Webhooks

For push delivery, verify each request before running the job:

```go
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
	fmt.Printf("job %s, attempt %d\n", job.JobID, job.Attempt)
})
```

Decode `job.Payload` into your own struct to read the data you attached to the schedule or job.

### Payload numbers

Numbers inside `Payload` and `Result` decode as `json.Number`, preserving the exact digits the API sent. The API reads numbers as 64-bit floats, so send integers above 2^53 as strings.

## Documentation

- [Package reference](https://pkg.go.dev/chronos.sh/go)
- [Chronos documentation](https://chronos.sh/docs)

## License

[MIT](LICENSE)
