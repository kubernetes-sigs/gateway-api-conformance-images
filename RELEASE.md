# Release Process

To release a new image:

* Cut a new tag
  * This will trigger a new [ProwJob](https://prow.k8s.io/?repo=kubernetes-sigs%2Fgateway-api-conformance-images&job=post-gateway-api-conformance-images-push-images) to publish images to the staging image repository.
* Once the job has finalized successfully, start the promotion process:
  * If you don't have a GitHub token, create one by going to your GitHub settings in [Personal access tokens](https://github.com/settings/tokens). Make sure you give the token the `repo` scope.
  * Create a PR to promote the images to the production registry:

      ```bash
      # Export the tag of the release to be cut, e.g.:
      export RELEASE_TAG=v0.0.1
      export GITHUB_TOKEN=<your GH token>
      make promote-images
      ```

* Merge the PR (/lgtm + /hold cancel) and verify the images are available in the production registry:
    - Wait for the [promotion prow job](https://prow.k8s.io/?repo=kubernetes%2Fk8s.io&job=post-k8sio-image-promo) to complete successfully. Then verify that the production images are accessible:

     ```bash
     docker pull registry.k8s.io/gateway-api/conformance/echo-basic:${RELEASE_TAG}
     docker pull registry.k8s.io/gateway-api/conformance/echo-advanced:${RELEASE_TAG}
     ```

**Notes**:
- `make promote-images` target tries to figure out your GitHub user handle in order to find the forked [k8s.io](https://github.com/kubernetes/k8s.io) repository.
If you have not forked the repo, please do it before running the Makefile target.
- If `make promote-images` fails with an error like `FATAL while checking fork of kubernetes/k8s.io` you may be able to solve it by manually setting the USER_FORK variable
i.e.  `export USER_FORK=<personal GitHub handle>`
- `kpromo` uses `git@github.com:...` as remote to push the branch for the PR. If you don't have `ssh` set up you can configure
git to use `https` instead via `git config --global url."https://github.com/".insteadOf git@github.com:`.
- This will automatically create a PR in [k8s.io](https://github.com/kubernetes/k8s.io) and assign the reviewers defined in `IMAGE_REVIEWERS`.

## After release steps

[Gateway API](https://github.com/kubernetes-sigs/gateway-api) uses this current repository on the following places that should be bumped:

* [go.mod](https://github.com/kubernetes-sigs/gateway-api/blob/main/conformance/go.mod)
* [conformance manifests](https://github.com/kubernetes-sigs/gateway-api/blob/main/conformance/base/manifests.yaml)

Some extra manifests may also use it. It is expected that dependabot runs and bumps the versions, but otherwise open a PR against Gateway API repository to bump the versions.
