package checkpoint

import eventlog "github.com/AadiJo/turnal/internal/events"

func (repo *Repo) EventLog() eventlog.Log {
	if repo == nil || repo.EventProducerID == "" {
		if repo == nil {
			return eventlog.Log{}
		}
		return eventlog.Open(repo.MetadataDir)
	}
	return eventlog.OpenFor(repo.MetadataDir, repo.WorkspaceRoot.String(), repo.RepoID, repo.StoreID, repo.WorktreeID, repo.EventProducerID)
}
