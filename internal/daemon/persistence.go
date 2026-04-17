package daemon

// HUB
// persistSession writes the session to the store if persistence is enabled.
// Must be called while d.mu is held (read or write lock).
func (d *Daemon) persistSession(ms *managedSession) {
	if d.Store == nil {
		return
	}
	if err := d.Store.UpsertSession(ms.info); err != nil {
		d.log.Printf("warning: persist session %s: %v", ms.info.ID, err)
	}
}

// HUB
// deletePersistedSession removes the session from the store if persistence is enabled.
func (d *Daemon) deletePersistedSession(id string) {
	if d.Store == nil {
		return
	}
	if err := d.Store.DeleteSession(id); err != nil {
		d.log.Printf("warning: delete persisted session %s: %v", id, err)
	}
}
