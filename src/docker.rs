use anyhow::{Context, Result};
use bollard::container::{InspectContainerOptions, ListContainersOptions, LogsOptions};
use bollard::Docker;
use futures_util::StreamExt;
use std::collections::HashMap;

#[derive(Debug, Clone)]
pub struct ContainerInfo {
    pub name: String,
    pub image: String,
    pub status: String,
    pub state: String,
}

pub struct DockerClient {
    client: Docker,
    runtime: String,
}

impl DockerClient {
    pub async fn new() -> Result<Self> {
        let client = Docker::connect_with_local_defaults()
            .with_context(|| "Failed to connect to Docker")?;

        // Determine runtime (docker vs podman)
        let info = client.info().await.with_context(|| "Failed to get Docker info")?;
        let runtime = if info
            .operating_system
            .as_ref()
            .map(|s| s.to_lowercase().contains("podman"))
            .unwrap_or(false)
        {
            "podman".to_string()
        } else {
            "docker".to_string()
        };

        Ok(Self { client, runtime })
    }

    pub fn runtime(&self) -> &str {
        &self.runtime
    }

    pub async fn list_running(&self) -> Result<Vec<ContainerInfo>> {
        let mut filters = HashMap::new();
        filters.insert("status", vec!["running"]);

        let options = ListContainersOptions {
            filters,
            ..Default::default()
        };

        let containers = self
            .client
            .list_containers(Some(options))
            .await
            .with_context(|| "Failed to list containers")?;

        let result: Vec<ContainerInfo> = containers
            .into_iter()
            .map(|c| {
                let name = c
                    .names
                    .as_ref()
                    .and_then(|n| n.first())
                    .map(|s| s.trim_start_matches('/').to_string())
                    .unwrap_or_default();

                let image = c.image.unwrap_or_default();
                let image = if image.len() > 40 {
                    image[..12].to_string()
                } else {
                    image
                };

                ContainerInfo {
                    name,
                    image,
                    status: c.status.unwrap_or_default(),
                    state: c.state.unwrap_or_default(),
                }
            })
            .collect();

        Ok(result)
    }

    /// Inspect a container by name. Returns None if container doesn't exist.
    pub async fn inspect_by_name(&self, name: &str) -> Result<Option<ContainerInfo>> {
        let options = InspectContainerOptions { size: false };

        match self.client.inspect_container(name, Some(options)).await {
            Ok(data) => {
                let state = data
                    .state
                    .as_ref()
                    .and_then(|s| s.status)
                    .map(|s| format!("{:?}", s).to_lowercase())
                    .unwrap_or_default();

                let status = data
                    .state
                    .as_ref()
                    .and_then(|s| s.status)
                    .map(|s| format!("{:?}", s))
                    .unwrap_or_default();

                let image = data
                    .config
                    .as_ref()
                    .and_then(|c| c.image.clone())
                    .unwrap_or_default();

                let image = if image.len() > 40 {
                    image[..12].to_string()
                } else {
                    image
                };

                Ok(Some(ContainerInfo {
                    name: data.name.unwrap_or_default().trim_start_matches('/').to_string(),
                    image,
                    status,
                    state,
                }))
            }
            Err(bollard::errors::Error::DockerResponseServerError {
                status_code: 404, ..
            }) => Ok(None),
            Err(e) => Err(e).with_context(|| format!("Failed to inspect container: {}", name)),
        }
    }

    /// Get container logs.
    pub async fn container_logs(&self, name: &str, tail: Option<usize>) -> Result<String> {
        let options = LogsOptions::<String> {
            stdout: true,
            stderr: true,
            tail: tail.map(|t| t.to_string()).unwrap_or_else(|| "all".to_string()),
            ..Default::default()
        };

        let mut stream = self.client.logs(name, Some(options));
        let mut output = String::new();

        while let Some(chunk) = stream.next().await {
            match chunk {
                Ok(log) => {
                    output.push_str(&log.to_string());
                }
                Err(e) => {
                    return Err(e).with_context(|| format!("Failed to read logs for {}", name));
                }
            }
        }

        Ok(output)
    }
}
