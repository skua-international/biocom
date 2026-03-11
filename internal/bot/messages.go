package bot

// Response messages used across command handlers.
const (
	MsgUnauthorized      = "BIOCOM: UNAUTHORIZED OPERATOR. ACCESS DENIED."
	MsgAttachmentMissing = "BIOCOM: ATTACHMENT NOT FOUND."
	MsgInvalidFilename   = "BIOCOM: INVALID FILENAME AFTER SANITIZATION."
	MsgDownloadFailure   = "BIOCOM: DOWNLOAD FAILURE."
	MsgFileAccessFailure = "BIOCOM: FILE ACCESS FAILURE."
	MsgBroadcastFailure  = "BIOCOM: BROADCAST FAILURE."
	MsgDockerNoRuntime   = "BIOCOM: DOCKER ACCESS FAILURE.\nNo container runtime connected."
)
