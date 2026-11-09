package model

import (
	"slices"
	"time"
)

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

type Permission string

const (
	PermissionNone  Permission = "none"
	PermissionRead  Permission = "read"
	PermissionWrite Permission = "write"
	PermissionOwner Permission = "owner"
)

type SubjectType string

const (
	SubjectUser          SubjectType = "user"
	SubjectGroup         SubjectType = "group"
	SubjectAnonymous     SubjectType = "anonymous"
	SubjectAuthenticated SubjectType = "authenticated"
)

type User struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	Role        Role      `json:"role"`
	Groups      []string  `json:"groups"`
	Source      string    `json:"source"`
	Disabled    bool      `json:"disabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Repository struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	OwnerID     string    `json:"ownerId"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Grant struct {
	RepositoryID string      `json:"repositoryId"`
	SubjectType  SubjectType `json:"subjectType"`
	Subject      string      `json:"subject"`
	Permission   Permission  `json:"permission"`
}

type Session struct {
	IDHash    string
	CSRFToken string
	UserID    string
	ExpiresAt time.Time
}

type AuditEvent struct {
	ID         int64     `json:"id"`
	ActorID    string    `json:"actorId"`
	Action     string    `json:"action"`
	TargetType string    `json:"targetType"`
	TargetID   string    `json:"targetId"`
	Before     string    `json:"before,omitempty"`
	After      string    `json:"after,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

const DefaultTheme = "midnight"

var Themes = []string{"midnight", "aurora", "ocean", "retro", "autumn", "light", "cyberpunk"}

func ValidTheme(theme string) bool {
	return Themes != nil && slices.Contains(Themes, theme)
}

type Settings struct {
	RegistrationEnabled bool   `json:"registrationEnabled"`
	Theme               string `json:"theme"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}
