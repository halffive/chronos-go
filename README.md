# chronos-go

Go client for [Chronos](https://chronos.sh): create recurring schedules and one-off jobs, then track their executions.

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

Chronos calls your endpoint on every run, and `Webhook.Verify` checks that the call came from Chronos. Standard library only. Requires Go 1.26 or later.

This module has no worker for pull delivery yet. To claim pull jobs, use the [Node.js SDK](https://www.npmjs.com/package/@chronos.sh/sdk) worker.

## Install

```sh
go get chronos.sh/go
```

## Quickstart

Create an API key in the [Chronos dashboard](https://app.chronos.sh) and export it as `CHRONOS_API_KEY`. This program creates a schedule that calls your endpoint every day at 02:00 London time, pauses and resumes it, runs it once now, and lists the job's delivery attempts:

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
	ctx := context.Background()
	client, err := chronos.NewClient(os.Getenv("CHRONOS_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	schedule, err := client.Schedules.Create(ctx, chronos.ScheduleCreateParams{
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

	if _, err := client.Schedules.Pause(ctx, schedule.ID); err != nil {
		log.Fatal(err)
	}
	if _, err := client.Schedules.Resume(ctx, schedule.ID); err != nil {
		log.Fatal(err)
	}

	job, err := client.Schedules.Trigger(ctx, schedule.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("job", job.ID, job.Status)

	page, err := client.Executions.List(ctx, chronos.ExecutionListParams{JobID: job.ID})
	if err != nil {
		log.Fatal(err)
	}
	for _, e := range page.Data {
		fmt.Println("attempt", e.ID, e.Status)
	}
}
```

Replace the URL with an endpoint you control. Each delivery attempt is an execution, so the list can be empty until Chronos first calls your endpoint. To block until the job finishes, use `client.Jobs.Wait`.

## Usage

The client has one field per resource: `Schedules`, `Jobs`, `Executions` and `Account`. Every method takes a `context.Context` first.

Optional request fields are pointers, and `new(value)` builds one. Update fields that can be cleared use `chronos.Nullable`:

```go
_, err := client.Schedules.Update(ctx, scheduleID, chronos.ScheduleUpdateParams{
	Name: new("Nightly report (EU)"), // set
	Runs: chronos.Null[int](),        // clear the run limit
})                                    // other fields stay as they are
```

`List` returns one page. `Iterate` walks every page:

```go
for job, err := range client.Jobs.Iterate(ctx, chronos.JobListParams{Status: chronos.JobStatusFailed}) {
	if err != nil {
		return err
	}
	fmt.Println(job.ID, job.Name)
}
```

Numbers inside `Payload` and `Result` decode as `json.Number`, with the exact digits the API sent. The API reads numbers as 64-bit floats, so send integers above 2^53 as strings.

### Errors

A rejected request returns `*chronos.APIError`. Branch on its `Code`:

```go
_, err := client.Schedules.Pause(ctx, scheduleID)
if apiErr, ok := errors.AsType[*chronos.APIError](err); ok && apiErr.Code == chronos.ErrorCodeScheduleAlreadyPaused {
	err = nil // already paused
}
```

`errors.Is(err, chronos.ErrNotFound)` tests the status, and `ErrUnauthorized` and `ErrRateLimited` work the same way.

A request that got no response returns `*chronos.NetworkError`. When an attempt times out, that error matches `chronos.ErrTimeout` and also `context.DeadlineExceeded`. Cancelling your own context, or reaching its deadline, returns `ctx.Err()` instead, so check `ctx.Err()` to tell the two apart. `Jobs.Wait` is the exception. At a deadline it returns a `*chronos.WaitTimeoutError`, which also matches `context.DeadlineExceeded`.

### Retries and timeouts

Each attempt times out after 30 seconds; change it with `chronos.WithRequestTimeout`. A deadline on your context bounds all attempts.

Any request rejected with 429 is retried, because the API rejects it before doing any work. GET requests are also retried after a network error, a timeout or a 5xx. Writes are not retried in those cases, so a lost response cannot create a schedule twice. Change the retry count with `chronos.WithMaxRequestRetries`.

### Webhooks

For push delivery, verify each request before running the job:

```go
webhook, err := chronos.NewWebhook([]string{os.Getenv("CHRONOS_SIGNING_SECRET")})
// In your handler, read the raw body with a size limit, then pass it and r.Header:
body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
if err != nil {
	http.Error(w, "unreadable body", http.StatusBadRequest)
	return
}
if err := webhook.Verify(body, r.Header); err != nil {
	http.Error(w, "invalid signature", http.StatusUnauthorized)
	return
}
```

## Documentation

- [Package reference](https://pkg.go.dev/chronos.sh/go)
- [Chronos documentation](https://chronos.sh/docs)

## License

[MIT](LICENSE)
