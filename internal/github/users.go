package github

import "context"

// UserName is a GitHub account's profile name, empty when they set none.
func (c *Client) UserName(ctx context.Context, installID, userID int64) (string, error) {
	cl, err := c.installation(installID)
	if err != nil {
		return "", err
	}
	u, _, err := cl.Users.GetByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.GetName(), nil
}
