package domain

type User struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	Email          string `json:"email"`
	Permissions    int    `json:"permissions"`
	HashedPassword string `json:"-"`
}
