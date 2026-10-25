package chronos

import (
	"context"
	"net/http"
	"time"
)

// Account is the workspace an API key belongs to.
type Account struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Timezone  string    `json:"timezone"`
	CreatedAt time.Time `json:"created_at"`
}

// AccountService reads the API key's account. Use it through [Client].
type AccountService service

// Get returns the account the client's API key belongs to.
func (s *AccountService) Get(ctx context.Context) (*Account, error) {
	return fetch[Account](ctx, s.client, apiRequest{method: http.MethodGet, path: "/v1/account"})
}
