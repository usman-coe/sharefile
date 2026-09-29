#Requires -RunAsAdministrator
# Removes the sharefile Scheduled Task and firewall rule.
Stop-ScheduledTask -TaskName sharefile -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName sharefile -Confirm:$false -ErrorAction SilentlyContinue
Stop-Process -Name sharefile -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName sharefile -ErrorAction SilentlyContinue
"sharefile removed"
