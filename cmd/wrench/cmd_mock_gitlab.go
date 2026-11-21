package main

import (
	"os"
	"strings"

	"github.com/ordo-systems/wrench/internal/app/mocks"
	"github.com/spf13/cobra"
)

// newMockGitLabCmd runs the GitLab mock; the Wrench configuration's users fill its account picker.
func newMockGitLabCmd() *cobra.Command {
	var o mocks.GitLabOptions
	c := &cobra.Command{
		Use:   "gitlab",
		Short: "Run the GitLab mock (OIDC with an account picker, Users API)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			o.ConfigPath = resolveConfig(c, o.ConfigPath)
			return mocks.RunGitLab(c.Context(), normalizeGitLab(o), os.LookupEnv)
		},
	}

	c.Flags().StringVar(&o.Listen, "listen", ":9000", "listen address")
	c.Flags().StringVar(&o.Issuer, "issuer", "http://127.0.0.1:9000", "issuer URL, the base of server-to-server endpoints")
	c.Flags().StringVar(&o.PublicURL, "public-url", "", "browser-facing base URL of the authorization endpoint (default the issuer)")
	configFlag(c, &o.ConfigPath)
	return c
}

// normalizeGitLab trims trailing slashes; an empty public URL means the issuer.
func normalizeGitLab(o mocks.GitLabOptions) mocks.GitLabOptions {
	o.Issuer = strings.TrimRight(o.Issuer, "/")
	o.PublicURL = strings.TrimRight(o.PublicURL, "/")
	if o.PublicURL == "" {
		o.PublicURL = o.Issuer
	}
	return o
}
