package store

// RaiseAlert records an alert under key. It returns true when the key is new, so the
// caller should tell the user; false when it was raised before.
func (s *Store) RaiseAlert(key string, ts int64, message string) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO alerts(key, ts, message) VALUES(?,?,?)`, key, ts, message)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Alert is a raised alert.
type Alert struct {
	Key     string
	TS      int64
	Message string
}

// Alerts returns the raised alerts, newest first.
func (s *Store) Alerts() ([]Alert, error) {
	rows, err := s.db.Query(`SELECT key, ts, message FROM alerts ORDER BY ts DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.Key, &a.TS, &a.Message); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
