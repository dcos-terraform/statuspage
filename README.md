# Description
Small web app to grab all our dcos-terraform repositories and
expose on one html page the status of the master and support branches.

# Build
```bash
make docker-build
```

# Run
```
docker run -p 8000:8000 -e LISTEN_PORT=8000 -e GITHUB_ACCESS_TOKEN=${GITHUB_ACCESS_TOKEN} -e GITHUB_ORG=dcos-terraform dcosterraform/statuspage
```
