package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvironmentsWith(t *testing.T) {
	config := fixture(t)
	all := config.All()
	require.Len(t, all, 2)

	// aws-alb is only in the aws environment, google/vpc only in the google one.
	got, err := environmentsWith(config, all, []string{"aws-alb"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "aws_exa", got[0].Name)

	got, err = environmentsWith(config, all, []string{"google/vpc"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "google_exa", got[0].Name)

	// Any of several modules qualifies an environment.
	got, err = environmentsWith(config, all, []string{"aws-alb", "google/vpc"})
	require.NoError(t, err)
	require.Len(t, got, 2)

	// External modules count too: route53 comes from the release source.
	got, err = environmentsWith(config, all, []string{"aws-v2/route53"})
	require.NoError(t, err)
	require.Len(t, got, 1, "route53 is only in aws_exa")
}
