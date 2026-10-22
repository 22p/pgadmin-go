package main

import (
	"time"
)

type SQLHistory struct {
	SQL      string
	Tag      string
	Err      string
	Duration time.Duration
	At       time.Time
}

type SQLFavorite struct {
	Name string
	SQL  string
}

type storedResults struct {
	ID   string
	Sets []ResultSet
	At   time.Time
}

const (
	maxHistory    = 30
	maxFavorites  = 30
	maxResultRows = 20000
)

func (s *Session) AddHistory(h SQLHistory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append([]SQLHistory{h}, s.history...)
	if len(s.history) > maxHistory {
		s.history = s.history[:maxHistory]
	}
}

func (s *Session) History() []SQLHistory {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SQLHistory, len(s.history))
	copy(out, s.history)
	return out
}

func (s *Session) ClearHistory() {
	s.mu.Lock()
	s.history = nil
	s.mu.Unlock()
}

func (s *Session) AddFavorite(name, sqlText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.favorites {
		if s.favorites[i].Name == name {
			s.favorites[i].SQL = sqlText
			return
		}
	}
	s.favorites = append([]SQLFavorite{{Name: name, SQL: sqlText}}, s.favorites...)
	if len(s.favorites) > maxFavorites {
		s.favorites = s.favorites[:maxFavorites]
	}
}

func (s *Session) RemoveFavorite(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.favorites[:0]
	for _, f := range s.favorites {
		if f.Name != name {
			out = append(out, f)
		}
	}
	s.favorites = out
}

func (s *Session) Favorites() []SQLFavorite {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SQLFavorite, len(s.favorites))
	copy(out, s.favorites)
	return out
}

// StoreResults 缓存最近一次查询结果,供结果导出使用;过大的结果不缓存。
func (s *Session) StoreResults(sets []ResultSet) string {
	rows := 0
	for _, set := range sets {
		rows += len(set.Rows)
	}
	if rows == 0 || rows > maxResultRows {
		return ""
	}
	id := randomToken(8)
	s.mu.Lock()
	s.lastResult = &storedResults{ID: id, Sets: sets, At: time.Now()}
	s.mu.Unlock()
	return id
}

func (s *Session) LastResultID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastResult == nil {
		return ""
	}
	return s.lastResult.ID
}

func (s *Session) Results(id string) ([]ResultSet, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastResult == nil || s.lastResult.ID != id {
		return nil, false
	}
	return s.lastResult.Sets, true
}
