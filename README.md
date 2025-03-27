The Canary Deployment Operator automates the process of safely rolling out new versions of applications by creating a secondary "canary" deployment alongside the primary
  deployment and gradually shifting traffic while monitoring metrics.

  The controller implements the reconciliation logic for CanaryDeployment custom resources. Here's how it works:

  1. Initialization Phase:
    - Creates a canary deployment based on the primary deployment
    - Sets up initial status with 0% traffic weight
  2. Progressive Traffic Shifting:
    - Follows configured steps to gradually increase traffic to canary
    - Updates replica counts proportionally to manage traffic distribution
    - Implements pauses between steps for observation
  3. Metric Analysis:
    - Integrates with Prometheus to evaluate metrics
    - Can analyze error rates, latency, CPU usage, etc.
    - Supports threshold-based and range-based validation
  4. Promotion/Rollback Logic:
    - Automatically promotes canary to primary if metrics pass
    - Rolls back or limits traffic if metrics fail
    - Supports manual promotion option
  5. Service Integration:
    - Updates service annotations to indicate traffic weight
    - Designed to work with service mesh implementations

  The reconciliation loop handles transitioning through various phases:
  - Initializing → Progressing → Analyzing → Promoting → Succeeded/Failed

  The controller uses Kubernetes controller-runtime patterns with owner references to manage resources and generate appropriate events for observability.

# Canary Deployment Operator

A Kubernetes operator that automates canary deployments by creating a secondary deployment with a small percentage of traffic and gradually increasing it based on metrics.

## Overview

The Canary Deployment Operator provides a safe and automated way to roll out new versions of your applications in a controlled manner. It creates a canary deployment alongside your primary deployment and gradually shifts traffic to it while monitoring metrics to ensure the new version is performing well.

Key features:
- Progressive traffic shifting with configurable steps
- Automatic metric analysis for key performance indicators
- Integration with Prometheus for metric collection
- Built-in safeguards for rollback if metrics degrade
- Support for both automatic and manual promotion

## Installation

### Prerequisites
- Kubernetes cluster v1.19+
- Prometheus installed in your cluster (for metric analysis)
- kubectl v1.19+
- Kustomize v3.8.7+

### Deploy the operator

```bash
# Clone the repository
git clone https://github.com/example/canary-operator.git
cd canary-operator

# Install CRDs
make install

# Deploy the operator
make deploy
```

## Usage

### Basic Example

1. Create your primary application deployment and service as usual:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: myapp
  namespace: default
spec:
  replicas: 3
  selector:
    matchLabels:
      app: myapp
  template:
    metadata:
      labels:
        app: myapp
    spec:
      containers:
      - name: myapp
        image: myapp:v1.0.0
        ports:
        - containerPort: 8080
---
apiVersion: v1