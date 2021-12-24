# Sesame-operator
Sesame Operator provides a method for packaging, deploying, and managing [Sesame][1]. The operator extends the
functionality of the Kubernetes API to create, configure, and manage instances of Sesame on behalf of users. It builds
upon the basic Kubernetes resource and controller concepts, but includes domain-specific knowledge to automate the
entire lifecycle of Sesame. Refer to the official [Kubernetes documentation][2] to learn more about the benefits of the
operator pattern.

## Get Started

### Prerequisites

* A [Kubernetes](https://kubernetes.io/) cluster
* [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/) installed

Install the Sesame Operator & Sesame CRDs:
```
$ kubectl apply -f https://raw.githubusercontent.com/projectsesame/sesame-operator/main/examples/operator/operator.yaml
```

Verify the deployment is available:
```
$ kubectl get deploy -n Sesame-operator
NAME               READY   UP-TO-DATE   AVAILABLE   AGE
Sesame-operator   1/1     1            1           1m
```

Install an instance of the `Sesame` custom resource:
```
$ kubectl apply -f https://raw.githubusercontent.com/projectsesame/sesame-operator/main/examples/Sesame/Sesame.yaml
```

Verify the `Sesame` custom resource is available:
```
$ kubectl get Sesame/Sesame-sample
NAME             READY   REASON
Sesame-sample   True    SesameAvailable
```

__Note:__ It may take several minutes for the `Sesame` custom resource to become available.

[Test with Ingress](https://projectsesame.io/docs/main/deploy-options/#test-with-ingress):
```
$ kubectl apply -f https://projectsesame.io/examples/kuard.yaml
```

Verify the example app deployment is available:
```
$ kubectl get deploy/kuard
NAME    READY   UP-TO-DATE   AVAILABLE   AGE
kuard   3/3     3            3           1m50s
```

Test the example app:
```
$ curl -o /dev/null -s -w "%{http_code}\n" http://local.projectsesame.io/
200
```

**Note:** A public DNS record exists for "local.projectsesame.io" which is
configured to resolve to `127.0.0.1`. This allows you to use a real domain name
when testing in a [kind](https://kind.sigs.k8s.io/) cluster. If testing on a
standard Kubernetes cluster, replace "local.projectsesame.io" with the
hostname of `kubectl get deploy/kuard`.

## Contributing

Thanks for taking the time to join our community and start contributing!

- Please familiarize yourself with the
[Code of Conduct](https://github.com/projectsesame/Sesame/blob/main/CODE_OF_CONDUCT.md) before contributing.
- See the [contributing guide](docs/CONTRIBUTING.md) for information about setting up your environment, the expected
workflow and instructions on the developer certificate of origin that is required.
- Check out the [open issues](https://github.com/projectsesame/sesame-operator/issues).
- Join the Sesame Slack channel: [#Sesame](https://kubernetes.slack.com/messages/Sesame/)
- Join the **Sesame Community Meetings** - [details can be found here](https://projectsesame.io/community)

[1]: https://projectsesame.io/
[2]: https://kubernetes.io/docs/concepts/extend-kubernetes/operator/
