package waylog

type User struct {
	ID     string
	Tier   string
	Region string
	VIP    bool
}

type StatsSnapshot struct {
	EventsEmitted   uint64
	EventsDropped   uint64
	ValidateFailed  uint64
	TransportErrors uint64
}
