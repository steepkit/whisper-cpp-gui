package server

import (
	"errors"
	"time"

	"github.com/steepkit/whisper-cpp-gui/internal/job"
)

const jobLeasePollInterval = 250 * time.Millisecond

// ModelLease is the path-bearing lifetime pin returned by the model layer.
type ModelLease interface {
	Path() string
	Close() error
}

// ModelAcquireFunc validates and pins one fixed-manifest logical model.
type ModelAcquireFunc func(name string) (ModelLease, error)

func (s *Server) retainModelLeases(jobID string, leases []ModelLease) {
	if len(leases) == 0 {
		return
	}
	held := append([]ModelLease(nil), leases...)
	s.activeModelLeases.Add(1)
	go func() {
		defer s.activeModelLeases.Done()
		defer s.closeModelLeases(held)
		ticker := time.NewTicker(jobLeasePollInterval)
		defer ticker.Stop()
		for {
			snapshot, err := s.jobs.Snapshot(jobID)
			if err != nil {
				if !errors.Is(err, job.ErrManagerClosed) && !errors.Is(err, job.ErrNotFound) {
					s.logger.Error("checking job before releasing model lease", "job_id", jobID, "error", err)
				}
				return
			}
			if terminalJobStatus(snapshot.Status) {
				return
			}
			<-ticker.C
		}
	}()
}

func (s *Server) closeModelLeases(leases []ModelLease) {
	for _, lease := range leases {
		if lease == nil {
			continue
		}
		if err := lease.Close(); err != nil {
			s.logger.Error("releasing model lease", "error", err)
		}
	}
}
