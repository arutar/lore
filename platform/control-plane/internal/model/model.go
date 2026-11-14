package model

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

var repositorySegment = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// ValidRepositoryName accepts "/"-separated segments of letters, digits, "-", "_" and ".".
func ValidRepositoryName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if !repositorySegment.MatchString(segment) {
			return false
		}
	}
	return true
}

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
	// DirectoryID is the objectGUID of an Active Directory account, empty for local ones.
	DirectoryID string `json:"directoryId,omitempty"`
	// DirectoryDisabled records that the directory reported the account disabled or deleted
	// at the last synchronization. It's separate from Disabled, which an administrator sets.
	DirectoryDisabled bool       `json:"directoryDisabled,omitempty"`
	GroupsSyncedAt    *time.Time `json:"groupsSyncedAt,omitempty"`
	// AccessDenied records that an Active Directory account isn't admitted: neither it nor any
	// group it belongs to (nested groups included) is on the access list. Local accounts are
	// always admitted; an administrator creates them.
	AccessDenied bool `json:"accessDenied,omitempty"`
	// Deleted marks a removed local account. The record stays, renamed, so the audit log keeps
	// its name; it's hidden from every list and can't be used.
	Deleted bool `json:"deleted,omitempty"`
}

// Federated reports whether the account comes from Active Directory.
func (u User) Federated() bool { return u.DirectoryID != "" }

// Active reports whether the account may be used at all.
func (u User) Active() bool {
	return !u.Disabled && !u.DirectoryDisabled && !u.AccessDenied && !u.Deleted
}

// AccessEntry is one Active Directory account or group on the list of who may use Lore. A
// group admits every member, directly or through nested groups, with the group's role.
// Without a matching entry an AD account can't sign in, whatever repository grants it has.
type AccessEntry struct {
	SubjectType SubjectType `json:"subjectType"` // SubjectUser or SubjectGroup
	Subject     string      `json:"subject"`     // the objectGUID
	Name        string      `json:"name"`
	Detail      string      `json:"detail,omitempty"`
	Role        Role        `json:"role"`
	Disabled    bool        `json:"disabled"`
	CreatedAt   time.Time   `json:"createdAt"`
}

// ResolveAccess decides what the entries give a directory account with these groups: a
// personal entry that is disabled shuts it out whatever its groups say; otherwise it's
// admitted by its own enabled entry or any enabled group entry, with the highest role among
// them.
func ResolveAccess(entries []AccessEntry, directoryID string, groups []string) (admitted bool, role Role) {
	role = RoleUser
	for _, entry := range entries {
		if entry.SubjectType == SubjectUser && strings.EqualFold(entry.Subject, directoryID) && entry.Disabled {
			return false, RoleUser
		}
	}
	for _, entry := range entries {
		if entry.Disabled {
			continue
		}
		match := false
		switch entry.SubjectType {
		case SubjectUser:
			match = directoryID != "" && strings.EqualFold(entry.Subject, directoryID)
		case SubjectGroup:
			for _, group := range groups {
				if strings.EqualFold(entry.Subject, group) {
					match = true
					break
				}
			}
		}
		if match {
			admitted = true
			if entry.Role == RoleAdmin {
				role = RoleAdmin
			}
		}
	}
	return admitted, role
}

// Admits reports whether the entries let a directory account with these groups in.
func Admits(entries []AccessEntry, directoryID string, groups []string) bool {
	admitted, _ := ResolveAccess(entries, directoryID, groups)
	return admitted
}

type Repository struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	OwnerID     string    `json:"ownerId"`
	OwnerName   string    `json:"ownerName,omitempty"`
	Public      bool      `json:"public"`
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
	IDToken   string `json:"-"`
	ExpiresAt time.Time
}

type AuditEvent struct {
	ID         int64  `json:"id"`
	ActorID    string `json:"actorId"`
	ActorName  string `json:"actorName,omitempty"`
	Action     string `json:"action"`
	TargetType string `json:"targetType"`
	TargetID   string `json:"targetId"`
	TargetName string `json:"targetName,omitempty"`
	// SubjectType and Subject name who a grant event applies to.
	SubjectType SubjectType `json:"subjectType,omitempty"`
	Subject     string      `json:"subject,omitempty"`
	SubjectName string      `json:"subjectName,omitempty"`
	Before      string      `json:"before,omitempty"`
	After       string      `json:"after,omitempty"`
	CreatedAt   time.Time   `json:"createdAt"`
}

const DefaultTheme = "midnight"

var Themes = []string{"midnight", "ocean", "retro", "autumn", "light", "cyberpunk", "sky", "sage", "ember"}

func ValidTheme(theme string) bool {
	return Themes != nil && slices.Contains(Themes, theme)
}

type Settings struct {
	RegistrationEnabled bool   `json:"registrationEnabled"`
	Theme               string `json:"theme"`
	// Language is the default interface language of the WebUI and the Windows client.
	Language string `json:"language"`
}

const DefaultLanguage = "en"

var Languages = []string{"en", "ru"}

func ValidLanguage(language string) bool {
	return slices.Contains(Languages, language)
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}
