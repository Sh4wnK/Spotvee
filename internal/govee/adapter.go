// Package govee adapts the shared Govee wrapper to Spotvee's device
// contract. It only uses the stable root API of github.com/Sh4wnK/Govee
// (NewClient, Devices, SetPower, SetBrightness, SetColorRGB), so the two
// repos stay decoupled.
package govee

import (
	"context"

	govee "github.com/Sh4wnK/Govee"
	"github.com/Sh4wnK/Spotvee/internal/devices"
)

// Controller implements devices.Controller over *govee.Client. The
// constructor performs no network I/O and never panics.
type Controller struct {
	client *govee.Client
}

// New builds the adapter. Options (WithHTTPClient, WithBaseURL, ...) are
// forwarded to the Govee client.
func New(apiKey string, opts ...govee.Option) *Controller {
	return &Controller{client: govee.NewClient(apiKey, opts...)}
}

// Devices lists all lights the API key can see.
func (c *Controller) Devices(ctx context.Context) ([]devices.Device, error) {
	ds, err := c.client.Devices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]devices.Device, 0, len(ds))
	for _, d := range ds {
		out = append(out, devices.Device{SKU: d.SKU, ID: d.ID, Name: d.Name})
	}
	return out, nil
}

// SetPower implements devices.Controller.
func (c *Controller) SetPower(ctx context.Context, sku, id string, on bool) error {
	return c.client.SetPower(ctx, sku, id, on)
}

// SetBrightness implements devices.Controller.
func (c *Controller) SetBrightness(ctx context.Context, sku, id string, percent int) error {
	return c.client.SetBrightness(ctx, sku, id, percent)
}

// SetColorRGB implements devices.Controller.
func (c *Controller) SetColorRGB(ctx context.Context, sku, id string, col devices.Color) error {
	return c.client.SetColorRGB(ctx, sku, id, govee.Color{R: col.R, G: col.G, B: col.B})
}
