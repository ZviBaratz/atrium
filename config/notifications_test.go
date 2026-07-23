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

// TestGetNotificationsFinished pins the attention ladder's deliberately restricted
// vocabulary: the finished-turn rung may only be "same", "off" or "bell". "desktop" and
// "osc" are peers of each other and both outrank "bell", so admitting them would make an
// inverted ladder (a finished turn louder than a blocked session) representable — they
// normalize to "same" exactly like a typo does.
func TestGetNotificationsFinished(t *testing.T) {
	require.Equal(t, NotificationsSame, (*Config)(nil).GetNotificationsFinished(), "nil config follows notifications")
	require.Equal(t, NotificationsSame, (&Config{}).GetNotificationsFinished(), "unset (old config) follows notifications")
	require.Equal(t, NotificationsSame, (&Config{NotificationsFinished: NotificationsSame}).GetNotificationsFinished())
	require.Equal(t, NotificationsOff, (&Config{NotificationsFinished: NotificationsOff}).GetNotificationsFinished(), "off leaves a finished turn to the unread marker")
	require.Equal(t, NotificationsBell, (&Config{NotificationsFinished: NotificationsBell}).GetNotificationsFinished())
	require.Equal(t, NotificationsSame, (&Config{NotificationsFinished: NotificationsDesktop}).GetNotificationsFinished(), "desktop is not a quieter rung")
	require.Equal(t, NotificationsSame, (&Config{NotificationsFinished: NotificationsOSC}).GetNotificationsFinished(), "osc is not a quieter rung")
	require.Equal(t, NotificationsSame, (&Config{NotificationsFinished: "bogus"}).GetNotificationsFinished(), "unknown normalizes to same")
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
