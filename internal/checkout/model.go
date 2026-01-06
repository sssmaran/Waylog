package checkout

type User struct {
	ID     string
	Tier   string // free | premium
	Region string
	VIP    bool
}

type CheckoutRequest struct {
	User User
}

type CheckoutResult struct {
	Success    bool
	StatusCode int
	ErrorCode  string
	ErrorMsg   string
	LatencyMs  int64
	Flow       string
	Flags      []string
}
