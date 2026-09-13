$workerDir = "C:\Projects\comfy-fleet-worker"
$taskName = "ComfyFleetWorker"

$arguments = "/c `"$workerDir\worker.exe`" >> `"$workerDir\worker.log`" 2>> `"$workerDir\worker.err.log`""

$action = New-ScheduledTaskAction `
    -Execute "cmd.exe" `
    -Argument $arguments `
    -WorkingDirectory $workerDir

Set-ScheduledTask `
    -TaskName $taskName `
    -Action $action

Write-Host "Updated scheduled task: $taskName"