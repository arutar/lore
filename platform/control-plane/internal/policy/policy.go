package policy

import (
	"errors"

	"github.com/arutar/lore/platform/control-plane/internal/model"
)

var ErrInvalidGrant = errors.New("invalid grant")

func Rank(permission model.Permission) int {
	switch permission {
	case model.PermissionRead:
		return 1
	case model.PermissionWrite:
		return 2
	case model.PermissionOwner:
		return 3
	default:
		return 0
	}
}

func Effective(user *model.User, grants []model.Grant) model.Permission {
	if user != nil && user.Disabled {
		return model.PermissionNone
	}
	if user != nil && user.Role == model.RoleAdmin {
		return model.PermissionOwner
	}

	permission := model.PermissionNone
	for _, grant := range grants {
		if !matches(user, grant) {
			continue
		}
		if Rank(grant.Permission) > Rank(permission) {
			permission = grant.Permission
		}
	}
	return permission
}

func CanRead(user *model.User, grants []model.Grant) bool {
	return Rank(Effective(user, grants)) >= Rank(model.PermissionRead)
}

func CanWrite(user *model.User, grants []model.Grant) bool {
	return Rank(Effective(user, grants)) >= Rank(model.PermissionWrite)
}

func CanManage(user *model.User, grants []model.Grant) bool {
	return Effective(user, grants) == model.PermissionOwner
}

func ValidateGrant(grant model.Grant) error {
	if Rank(grant.Permission) == 0 && grant.Permission != model.PermissionNone {
		return ErrInvalidGrant
	}

	switch grant.SubjectType {
	case model.SubjectAnonymous, model.SubjectAuthenticated:
		if grant.Subject != "" || (grant.Permission != model.PermissionNone && grant.Permission != model.PermissionRead) {
			return ErrInvalidGrant
		}
	case model.SubjectUser, model.SubjectGroup:
		if grant.Subject == "" || grant.Permission == model.PermissionNone {
			return ErrInvalidGrant
		}
	default:
		return ErrInvalidGrant
	}
	return nil
}

func matches(user *model.User, grant model.Grant) bool {
	switch grant.SubjectType {
	case model.SubjectAnonymous:
		return true
	case model.SubjectAuthenticated:
		return user != nil
	case model.SubjectUser:
		return user != nil && user.ID == grant.Subject
	case model.SubjectGroup:
		if user == nil {
			return false
		}
		for _, group := range user.Groups {
			if group == grant.Subject {
				return true
			}
		}
	}
	return false
}
