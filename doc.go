// Package chronos is the Go client for the Chronos API: recurring schedules
// and one-off jobs, delivered to your own code.
//
// Create a [Client] once and reuse it:
//
//	client, err := chronos.NewClient(os.Getenv("CHRONOS_API_KEY"))
//	if err != nil {
//		log.Fatal(err)
//	}
//	schedule, err := client.Schedules.Create(ctx, chronos.ScheduleCreateParams{
//		Name: "Nightly report",
//		Cron: new("0 2 * * *"),
//		Delivery: &chronos.Delivery{
//			Type: chronos.DeliveryTypePush,
//			HTTP: &chronos.HTTPDelivery{URL: "https://example.com/hooks/report", Method: "POST"},
//		},
//	})
//
// Resource types use the API's snake_case field names in JSON, so they encode
// to the objects the REST reference describes. In client responses, numbers
// inside Payload and Result decode as [encoding/json.Number], with the exact
// digits the API sent. The API reads numbers as 64-bit floats, so send
// integers above 2^53 as strings. Optional request fields are pointers; nil
// leaves the field out.
// Update fields that can be cleared use [Nullable].
//
// For push delivery, check each delivery with [Webhook.Verify], then decode its
// body into [PushedJob]. For pull delivery, run a [Worker].
//
// # Pull worker
//
// A [Worker] claims jobs and runs them one at a time until the context passed
// to [Worker.Run] ends:
//
//	worker := chronos.NewWorker(client)
//	worker.Handle("send-email", func(ctx context.Context, job *chronos.ClaimedJob) (map[string]any, error) {
//		return map[string]any{"sent": true}, nil
//	})
//	err := worker.Run(ctx)
//
// A job can run more than once, so make handlers idempotent, using
// ClaimedJob.JobID as the key. See [Handler] for what happens at a job's timeout
// and [Worker.Run] for shutdown.
//
// # Errors
//
// A rejected request returns an [*APIError]. Branch on its Code, or use
// [errors.Is] with [ErrUnauthorized], [ErrNotFound] or [ErrRateLimited].
//
// A request that got no response returns a [*NetworkError]. When an attempt
// times out, it matches [ErrTimeout] and also [context.DeadlineExceeded].
// Cancelling your own context, or reaching its deadline, returns ctx.Err()
// instead, so check ctx.Err() to tell the two apart. [JobsService.Wait] is the
// exception. At a deadline it returns a [*WaitTimeoutError], which also
// matches [context.DeadlineExceeded].
//
// # Retries and timeouts
//
// Each attempt is bounded by [WithRequestTimeout], 30 seconds by default, and
// a deadline on the context bounds all attempts. Any request rejected with 429
// is retried, because the API rejects it before doing any work. GET requests
// are also retried after a network error, a timeout or a 5xx. Writes are not
// retried in those cases, because the first attempt may have been applied.
//
// # Lists
//
// List methods return one [Page]. Iterate methods walk every page:
//
//	for job, err := range client.Jobs.Iterate(ctx, chronos.JobListParams{Status: chronos.JobStatusFailed}) {
//		if err != nil {
//			return err
//		}
//		fmt.Println(job.ID, job.Name)
//	}
package chronos
