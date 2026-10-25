package limen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"
)

type cacheSessionStore struct {
	cache  CacheAdapter
	schema *SessionSchema
	config *SchemaConfig
	prefix string
}

func newSecondarySessionStore(core *LimenCore) *cacheSessionStore {
	return &cacheSessionStore{
		cache:  core.CacheStore(),
		schema: core.Schema.Session,
		config: core.Schema,
		prefix: core.CacheKeyPrefix(),
	}
}

func (s *cacheSessionStore) sessionKey(token string) string {
	return fmt.Sprintf("%s:session:t:%s", s.prefix, token)
}

func (s *cacheSessionStore) userSessionsKey(userID any) string {
	return fmt.Sprintf("%s:session:u:%v", s.prefix, userID)
}

func (s *cacheSessionStore) Get(ctx context.Context, token string) (*Session, error) {
	stored, err := s.loadSession(ctx, token)
	if err != nil {
		return nil, err
	}
	return s.schema.FromStorage(stored).(*Session), nil
}

func (s *cacheSessionStore) Set(ctx context.Context, data any) error {
	switch v := data.(type) {
	case *Session:
		stored, err := s.loadSessionOrEmpty(ctx, v.Token)
		if err != nil {
			return err
		}

		maps.Copy(stored, s.schema.ToStorage(v))
		stored[s.schema.GetIDField()] = v.ID
		return s.saveSession(ctx, v.Token, stored)
	case map[SchemaField]any:
		token, err := sessionPayloadToken(v)
		if err != nil {
			return err
		}

		stored, err := s.loadSessionOrEmpty(ctx, token)
		if err != nil {
			return err
		}

		columns, err := s.schema.sessionColumns(v)
		if err != nil {
			return err
		}

		maps.Copy(stored, columns)
		return s.saveSession(ctx, token, stored)
	default:
		return errUnsupportedSessionData(data)
	}
}

func (s *cacheSessionStore) loadSessionOrEmpty(ctx context.Context, token string) (map[string]any, error) {
	stored, err := s.loadSession(ctx, token)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return make(map[string]any), nil
		}
		return nil, err
	}
	return stored, nil
}

func (s *cacheSessionStore) loadSession(ctx context.Context, token string) (map[string]any, error) {
	data, err := s.cache.Get(ctx, s.sessionKey(token))
	if errors.Is(err, ErrRecordNotFound) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}

	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	return s.rehydrateSession(stored)
}

func (s *cacheSessionStore) saveSession(ctx context.Context, token string, stored map[string]any) error {
	session := s.schema.FromStorage(stored).(*Session)

	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	ttl := max(time.Until(session.ExpiresAt), 0)
	if err := s.cache.Set(ctx, s.sessionKey(token), data, ttl); err != nil {
		return err
	}

	return s.addToUserIndex(ctx, session)
}

// rehydrateSession restores the Go types the JSON round trip degraded on the
// columns SessionSchema.FromStorage asserts.
func (s *cacheSessionStore) rehydrateSession(stored map[string]any) (map[string]any, error) {
	for _, column := range []string{s.schema.GetCreatedAtField(), s.schema.GetExpiresAtField(), s.schema.GetLastAccessField()} {
		switch v := stored[column].(type) {
		case time.Time:
		case string:
			parsed, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return nil, err
			}
			stored[column] = parsed
		default:
			return nil, fmt.Errorf("session store: column %q holds %T, want time", column, v)
		}
	}

	if _, ok := stored[s.schema.GetTokenField()].(string); !ok {
		return nil, fmt.Errorf("session store: column %q is not a string", s.schema.GetTokenField())
	}

	for _, column := range []string{s.schema.GetIDField(), s.schema.GetUserIDField()} {
		stored[column] = s.config.NormalizeIDValue(stored[column])
	}
	return stored, nil
}

func (s *cacheSessionStore) Delete(ctx context.Context, token string) error {
	sess, err := s.Get(ctx, token)
	if err != nil && !errors.Is(err, ErrSessionNotFound) {
		return err
	}

	if sess == nil {
		return nil
	}

	if err := s.cache.Delete(ctx, s.sessionKey(token)); err != nil {
		return err
	}

	return s.removeFromUserIndex(ctx, sess.UserID, token)
}

func (s *cacheSessionStore) ListByUserID(ctx context.Context, userID any) ([]Session, error) {
	return s.getUserSessions(ctx, userID)
}

func (s *cacheSessionStore) DeleteByUserID(ctx context.Context, userID any) error {
	sessions, err := s.getUserSessions(ctx, userID)
	if err != nil {
		return nil
	}

	for _, sess := range sessions {
		_ = s.cache.Delete(ctx, s.sessionKey(sess.Token))
	}

	return s.cache.Delete(ctx, s.userSessionsKey(userID))
}

func (s *cacheSessionStore) getUserSessions(ctx context.Context, userID any) ([]Session, error) {
	data, err := s.cache.Get(ctx, s.userSessionsKey(userID))
	if err != nil {
		return nil, err
	}

	var sessions []Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (s *cacheSessionStore) addToUserIndex(ctx context.Context, session *Session) error {
	sessions, _ := s.getUserSessions(ctx, session.UserID)

	for i, sess := range sessions {
		if sess.Token == session.Token {
			sessions[i] = *session
			return s.saveUserIndex(ctx, session.UserID, sessions)
		}
	}

	sessions = append(sessions, *session)
	return s.saveUserIndex(ctx, session.UserID, sessions)
}

func (s *cacheSessionStore) removeFromUserIndex(ctx context.Context, userID any, token string) error {
	sessions, err := s.getUserSessions(ctx, userID)
	if err != nil {
		return nil
	}

	filtered := sessions[:0]
	for _, sess := range sessions {
		if sess.Token != token {
			filtered = append(filtered, sess)
		}
	}

	if len(filtered) == 0 {
		return s.cache.Delete(ctx, s.userSessionsKey(userID))
	}

	return s.saveUserIndex(ctx, userID, filtered)
}

func (s *cacheSessionStore) saveUserIndex(ctx context.Context, userID any, sessions []Session) error {
	data, err := json.Marshal(sessions)
	if err != nil {
		return err
	}
	return s.cache.Set(ctx, s.userSessionsKey(userID), data, 0)
}
