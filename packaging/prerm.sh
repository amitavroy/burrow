#!/bin/sh
# Root cannot reach each user's login items or keychain, so only point at the command.
case "$1" in
remove|purge)
	echo "burrow: to drop your login items, sign-in and data, run 'syncd uninstall --purge' as each user (before removing the package if you can)." >&2
	;;
esac
exit 0
