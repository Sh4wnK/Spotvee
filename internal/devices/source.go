package devices

import (
	"context"
)

// Source adapts any device listing to the api.DeviceSource contract.
// It also feeds the engine's metadata cache.
type Source struct {
	ctrl Controller
}

// NewSource builds a source over a Controller.
func NewSource(ctrl Controller) *Source {
	return &Source{ctrl: ctrl}
}

// ListDevices returns all discoverable devices.
func (s *Source) ListDevices(ctx context.Context) ([]Device, error) {
	return s.ctrl.Devices(ctx)
}

// Refresh syncs device metadata into the engine.
func (s *Source) Refresh(ctx context.Context, eng interface {
	UpdateDevices([]Device)
}) error {
	list, err := s.ctrl.Devices(ctx)
	if err != nil {
		return err
	}
	eng.UpdateDevices(list)
	return nil
}
