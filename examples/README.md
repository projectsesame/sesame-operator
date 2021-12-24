# examples

This directory contains example manifests for running Sesame using Sesame
Operator. The following sections describe the purpose of each subdirectory.

## `Sesame`

An example instance of the `Sesame` custom resource. **Note:** You must first
run Sesame Operator using the manifest from the `operator` directory.

## `gateway`

Example manifests for managing Sesame using [Gateway API](https://gateway-api.sigs.k8s.io/). **Note:** You must first
run Sesame Operator using the manifest from the `operator` directory.

## `operator`

A single manifest rendered from individual `config` manifests suitable for
`kubectl apply`ing via a URL.
