"""Honor Notebook Controller's native URL prefix and require token auth."""
import os

if not os.environ.get("JUPYTER_TOKEN"):
    raise RuntimeError("the managed Jupyter token Secret must be mounted")
prefix = os.environ.get("NB_PREFIX", "/")
if not prefix.startswith("/") or ".." in prefix or "?" in prefix or "#" in prefix:
    raise ValueError("invalid native Notebook URL prefix")
os.execvp("jupyter", ["jupyter", "lab", "--ip=0.0.0.0", "--port=8888", "--no-browser",
    "--ServerApp.root_dir=/home/jovyan", "--ServerApp.base_url=" + prefix,
    "--ServerApp.show_banner=False", "--log-level=WARNING"])
