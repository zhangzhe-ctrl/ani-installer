# ANI offline package

Run on the installer node from the unpacked package directory:

```bash
sudo ./install.sh /absolute/path/cluster.yaml
```

After installation, verify the package:

```bash
sudo ./verify.sh /absolute/path/cluster.yaml
```

The installer reads the site-specific YAML, starts Hauler as `ani-image-registry.service`, verifies every image manifest, and invokes KubeKey. On the build machine, `HAULER_STORE` may point to an already verified Hauler store; otherwise `build-offline.sh` fetches from the sources listed in `ani/images.tsv`. Logs are written under `logs/<cluster-name>/install.log`. The Hauler registry remains running after installation so nodes can pull images during later Pod creation.
