package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/policy"
)

const maxBulkRows = 10_000

type bulkError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

type bulkResult struct {
	Imported int         `json:"imported"`
	Errors   []bulkError `json:"errors"`
}

type userRecord struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Email       string     `json:"email"`
	DisplayName string     `json:"displayName"`
	Role        model.Role `json:"role"`
	Groups      []string   `json:"groups"`
	Disabled    bool       `json:"disabled"`
}

func (s *Server) exportGrants(w http.ResponseWriter, r *http.Request) {
	repository, grants, ok := s.manageableRepository(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", repository.Name+"-grants.csv"))
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"subjectType", "subject", "permission"})
		for _, grant := range grants {
			_ = writer.Write([]string{string(grant.SubjectType), grant.Subject, string(grant.Permission)})
		}
		writer.Flush()
		return
	}
	writeJSON(w, http.StatusOK, grants)
}

func (s *Server) importGrants(w http.ResponseWriter, r *http.Request) {
	repository, _, ok := s.manageableRepository(w, r)
	if !ok {
		return
	}
	actor, _ := currentUser(r.Context())
	result := bulkResult{Errors: []bulkError{}}
	apply := func(line int, grant model.Grant) {
		grant.RepositoryID = repository.ID
		grant.Subject = strings.TrimSpace(grant.Subject)
		if grant.Permission == model.PermissionNone {
			if err := s.store.RemoveGrant(r.Context(), actor.ID, grant); err != nil {
				result.Errors = append(result.Errors, bulkError{Line: line, Message: "Couldn't remove grant"})
				return
			}
			result.Imported++
			return
		}
		if err := policy.ValidateGrant(grant); err != nil {
			result.Errors = append(result.Errors, bulkError{Line: line, Message: "Grant is invalid"})
			return
		}
		valid, err := s.validGrantSubject(r.Context(), grant)
		if err != nil {
			result.Errors = append(result.Errors, bulkError{Line: line, Message: "Couldn't validate user or group"})
			return
		}
		if !valid {
			result.Errors = append(result.Errors, bulkError{Line: line, Message: "User or group doesn't exist"})
			return
		}
		if err := s.store.SetGrant(r.Context(), actor.ID, grant); err != nil {
			result.Errors = append(result.Errors, bulkError{Line: line, Message: "Couldn't save grant"})
			return
		}
		result.Imported++
	}

	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") {
		err = readCSV(r, func(line int, record []string) {
			if len(record) != 3 {
				result.Errors = append(result.Errors, bulkError{Line: line, Message: "Expected subjectType,subject,permission"})
				return
			}
			apply(line, model.Grant{SubjectType: model.SubjectType(record[0]), Subject: record[1], Permission: model.Permission(record[2])})
		})
	} else {
		var grants []model.Grant
		if err = decodeBulkJSON(w, r, &grants); err == nil {
			if len(grants) > maxBulkRows {
				err = errors.New("too many rows")
			} else {
				for index, grant := range grants {
					apply(index+1, grant)
				}
			}
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_import", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) exportUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.allUsers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=lore-users.csv")
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"id", "username", "email", "displayName", "role", "groups", "disabled"})
		for _, user := range users {
			_ = writer.Write([]string{user.ID, user.Username, user.Email, user.DisplayName, string(user.Role), strings.Join(user.Groups, ";"), strconv.FormatBool(user.Disabled)})
		}
		writer.Flush()
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) importUsers(w http.ResponseWriter, r *http.Request) {
	actor, _ := currentUser(r.Context())
	result := bulkResult{Errors: []bulkError{}}
	apply := func(line int, record userRecord) {
		record.ID = strings.TrimSpace(record.ID)
		record.Username = strings.TrimSpace(record.Username)
		if record.ID == "" || record.Username == "" || len(record.ID) > 512 || len(record.Username) > 256 {
			result.Errors = append(result.Errors, bulkError{Line: line, Message: "ID and username are required"})
			return
		}
		if record.Role == "" {
			record.Role = model.RoleUser
		}
		if record.Role != model.RoleAdmin && record.Role != model.RoleUser {
			result.Errors = append(result.Errors, bulkError{Line: line, Message: "Role must be admin or user"})
			return
		}
		if actor.ID == record.ID && (record.Disabled || record.Role != model.RoleAdmin) {
			result.Errors = append(result.Errors, bulkError{Line: line, Message: "You can't disable or demote your own account"})
			return
		}
		user, err := s.store.UpsertUser(r.Context(), model.User{
			ID: record.ID, Username: record.Username, Email: record.Email, DisplayName: record.DisplayName,
			Role: record.Role, Groups: record.Groups, Source: "import",
		})
		if err == nil && (user.Role != record.Role || user.Disabled != record.Disabled) {
			user, err = s.store.UpdateUser(r.Context(), actor.ID, user.ID, record.Role, record.Disabled)
		}
		if err != nil {
			result.Errors = append(result.Errors, bulkError{Line: line, Message: "Couldn't save user"})
			return
		}
		result.Imported++
	}

	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") {
		err = readCSV(r, func(line int, record []string) {
			if len(record) != 7 {
				result.Errors = append(result.Errors, bulkError{Line: line, Message: "Expected id,username,email,displayName,role,groups,disabled"})
				return
			}
			disabled, parseErr := strconv.ParseBool(record[6])
			if parseErr != nil {
				result.Errors = append(result.Errors, bulkError{Line: line, Message: "Disabled must be true or false"})
				return
			}
			apply(line, userRecord{ID: record[0], Username: record[1], Email: record[2], DisplayName: record[3], Role: model.Role(record[4]), Groups: splitGroups(record[5]), Disabled: disabled})
		})
	} else {
		var users []userRecord
		if err = decodeBulkJSON(w, r, &users); err == nil {
			if len(users) > maxBulkRows {
				err = errors.New("too many rows")
			} else {
				for index, user := range users {
					apply(index+1, user)
				}
			}
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_import", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) allUsers(ctx context.Context) ([]model.User, error) {
	users := []model.User{}
	cursor := ""
	for {
		page, err := s.store.ListUsers(ctx, cursor, 100)
		if err != nil {
			return nil, err
		}
		users = append(users, page.Items...)
		if page.NextCursor == "" {
			return users, nil
		}
		cursor = page.NextCursor
	}
}

func decodeBulkJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("expected a JSON array")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON array")
	}
	return nil
}

func readCSV(r *http.Request, apply func(int, []string)) error {
	reader := csv.NewReader(io.LimitReader(r.Body, maxJSONBody+1))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return errors.New("CSV header is missing")
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff")
	}
	line := 1
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		line++
		if err != nil {
			return errors.New("CSV is malformed")
		}
		if line > maxBulkRows+1 {
			return errors.New("too many rows")
		}
		apply(line, record)
	}
}

func splitGroups(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, ";")
}
