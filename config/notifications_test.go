package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetNotifications(t *testing.T) {
	require.Equal(t, NotificationsOff, (*Config)(nil).GetNotifications(), "nil config defaults to off")
	require.Equal(t, NotificationsOff, (&Config{}).GetNotifications(), "empty value (old config) defaults to off")
	require.Equal(t, NotificationsOff, (&Config{Notifications: "bogus"}).GetNotifications(), "unknown normalizes to off")
	require.Equal(t, NotificationsBell, (&Config{Notifications: NotificationsBell}).GetNotifications())
	require.Equal(t, NotificationsDesktop, (&Config{Notifications: NotificationsDesktop}).GetNotifications())
	require.Equal(t, NotificationsOSC, (&Config{Notifications: NotificationsOSC}).GetNotifications(), "osc is a valid mode")
}

func TestGetNotifyWhenFocused(t *testing.T) {
	require.False(t, (*Config)(nil).GetNotifyWhenFocused(), "nil config keeps focus-gating on (silent while focused)")
	require.False(t, (&Config{}).GetNotifyWhenFocused(), "unset (old config) keeps focus-gating on")
	require.True(t, (&Config{NotifyWhenFocused: true}).GetNotifyWhenFocused(), "explicit true still notifies while focused")
}

func TestGetNotifyCommand(t *testing.T) {
	require.Equal(t, "", (*Config)(nil).GetNotifyCommand(), "nil config yields empty command")
	require.Equal(t, "", (&Config{}).GetNotifyCommand(), "unset command is empty")
	require.Equal(t, "notify-send x", (&Config{NotifyCommand: "notify-send x"}).GetNotifyCommand())
}
