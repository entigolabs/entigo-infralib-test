#!/usr/bin/env python3
# SPDX SBOMs for the OCI packages of a module repository. Adapted from
# entigo-infralib's common/sbom.py: the registry and the supplier come from the
# environment instead of being entigo-infralib's.
#
#   SBOM_REGISTRY  the primary registry path packages are pushed under,
#                  e.g. ghcr.io/entigolabs/entigo-infralib-example-release
#   SBOM_SUPPLIER  organisation name for the supplier fields
#   SBOM_IMAGES    optional, for the index: the release's container images
#                  as manifest.json lists them ("images")
#
# Usage:
#   Helm:     sbom.py helm <chart_dir> <chart_name> <version>
#   OpenTofu: sbom.py tofu <module_dir> <module_name> <version> <module_type>
#   Index:    sbom.py agent <entries_jsonl> <version> <sbom_dir>
#             sbom_dir holds the SBOM of every package, as publish.sh names
#             them: <chart>-sbom.spdx.json, <type>-<module>-sbom.spdx.json

import glob
import hashlib
import json
import re
import subprocess
import sys
import os
import tempfile
import uuid
from datetime import datetime, timezone

REGISTRY = os.environ.get("SBOM_REGISTRY", "").rstrip("/")
SUPPLIER = os.environ.get("SBOM_SUPPLIER", "Entigolabs")


def make_document(name, version, namespace_suffix):
    # Base SPDX 2.3 document structure
    return {
        "spdxVersion": "SPDX-2.3",
        "dataLicense": "CC0-1.0",
        "SPDXID": "SPDXRef-DOCUMENT",
        "name": f"{name}-{version}",
        "documentNamespace": f"https://{REGISTRY}/sbom/{namespace_suffix}/{version}/{uuid.uuid4()}",
        "creationInfo": {
            "licenseListVersion": "3.28",
            "creators": [f"Organization: {SUPPLIER}", "Tool: entigo-infralib-test-sbom"],
            "created": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        },
        "packages": [],
        "relationships": []
    }


def add_root_package(doc, spdx_id, name, version, download_location, package_type, purl):
    # Root package — DOCUMENT DESCRIBES root
    doc["packages"].append({
        "name": name,
        "SPDXID": spdx_id,
        "versionInfo": version,
        "supplier": f"Organization: {SUPPLIER}",
        "downloadLocation": download_location,
        "filesAnalyzed": False,
        "licenseConcluded": "NOASSERTION",
        "licenseDeclared": "NOASSERTION",
        "copyrightText": "NOASSERTION",
        "primaryPackagePurpose": package_type,
        "externalRefs": [
            {
                "referenceCategory": "PACKAGE-MANAGER",
                "referenceType": "purl",
                "referenceLocator": purl
            }
        ]
    })
    doc["relationships"].append({
        "spdxElementId": "SPDXRef-DOCUMENT",
        "relatedSpdxElement": spdx_id,
        "relationshipType": "DESCRIBES"
    })


def add_dependency(doc, root_spdx_id, spdx_id, name, version, download_location,
                   package_type, purl, comment=None, relationship="DEPENDS_ON", checksum=None):
    # Dependency package
    pkg = {
        "name": name,
        "SPDXID": spdx_id,
        "supplier": "NOASSERTION",
        "downloadLocation": download_location,
        "filesAnalyzed": False,
        "licenseConcluded": "NOASSERTION",
        "licenseDeclared": "NOASSERTION",
        "copyrightText": "NOASSERTION",
        "primaryPackagePurpose": package_type,
        "externalRefs": [
            {
                "referenceCategory": "PACKAGE-MANAGER",
                "referenceType": "purl",
                "referenceLocator": purl
            }
        ]
    }
    # Omit versionInfo if not known — "latest" or empty string defeats SBOM purpose
    if version:
        pkg["versionInfo"] = version
    if comment:
        pkg["comment"] = comment
    # "sha256:<hex>", the digest an image is pinned by
    if checksum and checksum.startswith("sha256:"):
        pkg["checksums"] = [{"algorithm": "SHA256", "checksumValue": checksum.split(":", 1)[1]}]
    doc["packages"].append(pkg)

    # BUILD_TOOL_OF direction is reversed: tool BUILD_TOOL_OF module
    if relationship == "BUILD_TOOL_OF":
        doc["relationships"].append({
            "spdxElementId": spdx_id,
            "relatedSpdxElement": root_spdx_id,
            "relationshipType": "BUILD_TOOL_OF"
        })
    else:
        doc["relationships"].append({
            "spdxElementId": root_spdx_id,
            "relatedSpdxElement": spdx_id,
            "relationshipType": relationship
        })


def safe_spdx_id(name):
    # SPDX IDs only allow letters, numbers and dashes
    return re.sub(r"[^a-zA-Z0-9\-]", "-", name)


def split_image_ref(image):
    # "<repository>[:<tag>][@<digest>]" -> (repository, tag, digest), tag and
    # digest None when absent. A colon before the last slash is a registry
    # port, not a tag.
    ref, _, digest = image.partition("@")
    tag = None
    if ":" in ref.split("/")[-1]:
        ref, tag = ref.rsplit(":", 1)
    return ref, tag, digest or None


def load_yaml(path=None, text=None):
    # yq is the YAML parser every publishing environment already has.
    out = subprocess.run(["yq", "-o", "json", ".", path or "-"], input=text,
                         capture_output=True, text=True, check=True).stdout
    return json.loads(out) or {}


def merge(base, over):
    # Helm's coalescing: the parent chart's values win over a subchart's defaults.
    for k, v in over.items():
        base[k] = merge(base[k], v) if isinstance(v, dict) and isinstance(base.get(k), dict) else v
    return base


def disabled_flags(values, path=()):
    # Every "enabled: false" of the values, as a path of keys.
    for k, v in values.items():
        if isinstance(v, dict):
            yield from disabled_flags(v, path + (k,))
        elif k == "enabled" and v is False:
            yield path + (k,)


def enable(paths):
    # A values overlay that turns the given flags on.
    overlay = {}
    for path in paths:
        node = overlay
        for key in path[:-1]:
            node = node.setdefault(key, {})
        node[path[-1]] = True
    return overlay


def find_images(rendered):
    # {image: {the subcharts whose templates use it, None for the chart's own}}.
    # helm template names the template of every manifest:
    # "# Source: <chart>/templates/..." or "# Source: <chart>/charts/<subchart>/...".
    images, owner, source, injector = {}, None, "", False
    # An operator starts images that are in its configuration, not in a pod
    # spec (trivy-operator's scan jobs and node collector): a ConfigMap's
    # "<x>.repository" with its "<x>.tag", and a key that ends in "imageRef".
    repositories, tags = {}, {}

    def configured():
        for key in repositories.keys() & tags.keys():
            images.setdefault(f"{repositories[key]}:{tags[key]}", set()).add(owner)
        repositories.clear()
        tags.clear()

    for line in rendered.splitlines() + ["---"]:
        if line == "---":
            configured()
            continue
        if line.startswith("# Source: "):
            source = line[len("# Source: "):]
            parts = source.split("/")
            owner = parts[2] if len(parts) > 2 and parts[1] == "charts" else None
            continue
        found = re.findall(r"image:\s*['\"]?([^\s'\"]+)['\"]?", line)
        found += re.findall(r"^\s*[\w.]*imageRef:\s*['\"]?([^\s'\"]+)['\"]?\s*$", line)
        part = re.match(r"\s*([\w.]+)\.(repository|tag):\s*['\"]?([^\s'\"]+)['\"]?\s*$", line)
        if part:
            (repositories if part.group(2) == "repository" else tags)[part.group(1)] = part.group(3)
        # Crossplane packages (Provider, Function, Configuration spec.package)
        # are OCI images too.
        found += re.findall(r"^\s*package:\s*['\"]?([^\s'\"]+/[^\s'\"]+)['\"]?\s*$", line)
        for image in found:
            # Not images: "auto" is Istio's placeholder for the proxy its
            # webhook injects, and its injection templates, which helm passes
            # through in a ConfigMap, have "{{ ... }}" where the image goes.
            if image != "auto" and re.fullmatch(r"[\w.\-/:@]+", image):
                images.setdefault(image, set()).add(owner)
        if line.startswith("kind: ConfigMap") and source.endswith("/istiod-injector-configmap.yaml"):
            injector = owner
    if injector is not False:
        images.setdefault(istio_proxy_image(rendered), set()).add(injector)
    return images


def istio_proxy_image(rendered):
    # The sidecar and gateway proxy is in no manifest: istiod injects it into
    # the pods of the mesh, composing its name from the values the injector
    # ConfigMap carries, the way Istio's ProxyImage does.
    configs = subprocess.run(
        ["yq", "ea", "-o", "json", '[select(.kind == "ConfigMap" and .data.values != null) | .data.values]', "-"],
        input=rendered, capture_output=True, text=True, check=True).stdout
    g = json.loads(json.loads(configs)[0])["global"]
    image = g["proxy"]["image"]
    if "/" in image:
        return image
    return f"{g['hub']}/{image}:{g['tag']}" + (f"-{g['variant']}" if g.get("variant") else "")


def chart_images(chart_dir, dependencies):
    # {image: ([what it was rendered with], {the subcharts that use it})}. A chart deploys different images
    # depending on its values, and the SBOM lists all it can deploy: the
    # default render, one render per cloud (values-<cloud>.yaml, which the
    # agent adds on that cloud) and one with every "enabled: false" switched
    # on, for the optional components.
    base = []
    # The digests the module pins its images by, as the agent adds this
    # value file to the Application, so the SBOM lists what is deployed.
    # Some static tests require test/static_values.yaml to render; include
    # it when present (the test folder still exists at SBOM time).
    for values in ("values-images.yaml", "test/static_values.yaml"):
        if os.path.isfile(f"{chart_dir}/{values}"):
            base += ["-f", f"{chart_dir}/{values}"]

    def render(*values):
        # The images of one render, None when the chart does not render.
        cmd = ["helm", "template", chart_dir]
        for v in values:
            cmd += ["-f", v]
        result = subprocess.run(cmd + base, capture_output=True, text=True)
        if result.returncode != 0:
            return None, result.stderr.strip()
        return find_images(result.stdout), None

    found = {}
    def add(what, images):
        for image, owners in images.items():
            renders, all_owners = found.setdefault(image, ([], set()))
            renders.append(what)
            all_owners |= owners

    renders = [("default values", [])]
    renders += [(os.path.basename(p), [p]) for p in sorted(glob.glob(f"{chart_dir}/values-*.yaml"))
                if os.path.basename(p) != "values-images.yaml"]
    for what, values in renders:
        images, error = render(*values)
        if images is None:
            # A cloud's render can need values only the agent has there.
            print(f"Warning: helm template of {chart_dir} with {what} failed, its images are not listed: {error}", file=sys.stderr)
        else:
            add(what, images)

    # The values helm renders with: the subcharts' defaults under their name
    # or alias, overridden by the chart's own values.
    values = {}
    for dep in dependencies:
        packed = glob.glob(f"{chart_dir}/charts/{dep['name']}-{dep['version']}.tgz") or [f"{chart_dir}/charts/{dep['name']}"]
        shown = subprocess.run(["helm", "show", "values", packed[0]], capture_output=True, text=True)
        if shown.returncode == 0:
            values[dep.get("alias") or dep["name"]] = load_yaml(text=shown.stdout)
    if os.path.isfile(f"{chart_dir}/values.yaml"):
        merge(values, load_yaml(f"{chart_dir}/values.yaml"))

    def optional(flags):
        # The images with these flags on. Charts refuse some combinations
        # (two exclusive options, an option that needs more values), so a set
        # that does not render is halved until its parts do; a flag that does
        # not render alone is left out.
        if not flags:
            return {}
        with tempfile.NamedTemporaryFile("w", suffix=".json") as overlay:
            json.dump(enable(flags), overlay)  # JSON is YAML
            overlay.flush()
            images, _ = render(overlay.name)
        if images is not None:
            return images
        if len(flags) == 1:
            return {}
        images = optional(flags[:len(flags) // 2])
        for image, owners in optional(flags[len(flags) // 2:]).items():
            images.setdefault(image, set()).update(owners)
        return images

    add("optional components enabled",
        {i: o for i, o in optional(sorted(disabled_flags(values))).items() if i not in found})
    return found


def generate_helm_sbom(chart_dir, chart_name, version):
    doc = make_document(chart_name, version, f"k8s/{chart_name}")

    root_id  = "SPDXRef-chart-root"
    repo_url = f"{REGISTRY}/k8s/{chart_name}"
    add_root_package(
        doc, root_id, chart_name, version,
        download_location=f"oci://{repo_url}",
        package_type="INSTALL",
        # Digest not available at SBOM generation time — tag only
        purl=f"pkg:oci/{chart_name}?repository_url={repo_url}&tag={version}"
    )

    dependencies = [d for d in load_yaml(f"{chart_dir}/Chart.yaml").get("dependencies") or []
                    if d.get("name") and d.get("version")]
    # A chart wrapped more than once, under aliases, is one package.
    for dep_name, dep_version, dep_repo in sorted({(d["name"], str(d["version"]), d.get("repository") or "NOASSERTION")
                                                   for d in dependencies}):
        add_dependency(
            doc, root_id, f"SPDXRef-helm-{safe_spdx_id(dep_name)}", dep_name, dep_version,
            download_location=dep_repo,
            package_type="INSTALL",
            purl=f"pkg:generic/{dep_name}@{dep_version}?download_url={dep_repo}"
        )

    # The SBOM is a tree: the module depends on the charts it wraps and on the
    # images of its own templates, a wrapped chart on the images of its.
    # A subchart is rendered under its alias, when it has one.
    chart_ids = {d.get("alias") or d["name"]: f"SPDXRef-helm-{safe_spdx_id(d['name'])}" for d in dependencies}
    for image, (rendered_with, owners) in sorted(chart_images(chart_dir, dependencies).items()):
        img_ref, tag, digest = split_image_ref(image)
        if not tag and not digest:
            tag = "latest"
        spdx_id = f"SPDXRef-image-{safe_spdx_id(img_ref.replace('/', '-'))}"

        parts = img_ref.split("/")
        if parts[0] in ("docker.io", "index.docker.io"):
            if len(parts) > 2 and parts[1] == "library":
                img_url = f"https://hub.docker.com/_/{parts[2]}"
            else:
                img_url = f"https://hub.docker.com/r/{'/'.join(parts[1:])}"
        elif "/" not in img_ref:
            img_url = f"https://hub.docker.com/_/{img_ref}"
        else:
            img_url = f"https://{img_ref}"

        # A pinned image is identified by its digest: the purl carries it
        # as the version, and the tag, when there is one, stays a qualifier.
        purl = f"pkg:oci/{img_ref.split('/')[-1]}"
        if digest:
            purl += f"@{digest}"
        purl += f"?repository_url={img_ref}"
        if tag:
            purl += f"&tag={tag}"
        used_by = sorted({chart_ids.get(owner, root_id) for owner in owners})
        add_dependency(
            doc, used_by[0], spdx_id, img_ref, tag or digest,
            download_location=img_url,
            package_type="CONTAINER",
            purl=purl,
            comment="Rendered with: " + ", ".join(rendered_with),
            checksum=digest
        )
        for other in used_by[1:]:
            doc["relationships"].append({
                "spdxElementId": other,
                "relatedSpdxElement": spdx_id,
                "relationshipType": "DEPENDS_ON"
            })

    return doc


def generate_tofu_sbom(module_dir, module_name, version, module_type):
    doc = make_document(module_name, version, f"{module_type}/{module_name}")

    root_id  = "SPDXRef-module-root"
    repo_url = f"{REGISTRY}/{module_type}/{module_name}"
    add_root_package(
        doc, root_id, module_name, version,
        download_location=f"oci://{repo_url}",
        package_type="LIBRARY",
        purl=f"pkg:oci/{module_name}?repository_url={repo_url}&tag={version}"
    )

    versions_tf = f"{module_dir}/versions.tf"
    try:
        with open(versions_tf) as f:
            content = f.read()
    except FileNotFoundError:
        print(f"Warning: versions.tf not found in {module_dir}", file=sys.stderr)
        add_vendored_modules(doc, root_id, module_dir)
        return doc

    # Parse required_providers blocks
    providers = re.findall(
        r'(\w+)\s*=\s*\{[^}]*source\s*=\s*"([^"]+)"[^}]*version\s*=\s*"([^"]+)"',
        content
    )

    for alias, source, prov_version in providers:
        namespace, prov_name = source.split("/") if "/" in source else ("", source)
        spdx_id = f"SPDXRef-provider-{safe_spdx_id(alias)}"
        # Fold namespace into name: hashicorp/aws -> hashicorp-aws
        purl_name = f"{namespace}-{prov_name}" if namespace else prov_name
        add_dependency(
            doc, root_id, spdx_id, source, prov_version,
            download_location=f"https://registry.terraform.io/providers/{source}/{prov_version}",
            package_type="LIBRARY",
            purl=f"pkg:generic/{purl_name}@{prov_version}?download_url=https://registry.terraform.io/providers/{source}/{prov_version}"
        )

    # OpenTofu is a build tool, not a shipped dependency
    # versionInfo omitted — required_version is a constraint not a pinned version
    tofu_version_match = re.search(r'required_version\s*=\s*"([^"]+)"', content)
    if tofu_version_match:
        constraint = tofu_version_match.group(1)
        add_dependency(
            doc, root_id, "SPDXRef-opentofu", "opentofu", None,
            download_location="https://opentofu.org",
            package_type="APPLICATION",
            purl="pkg:generic/opentofu",
            comment=f"Required version constraint: {constraint}",
            relationship="BUILD_TOOL_OF"
        )

    add_vendored_modules(doc, root_id, module_dir)
    return doc


def add_vendored_modules(doc, root_id, module_dir):
    # The community modules shipped inside the package under vendors/, from
    # the vendor.lock that scripts/vendor.sh writes: a list of flat
    # "key: value" entries, each starting with "- name:".
    try:
        with open(f"{module_dir}/vendor.lock") as f:
            lines = f.read().splitlines()
    except FileNotFoundError:
        return
    entries = []
    for line in lines:
        m = re.match(r'^\s*(-\s+)?(\w+):\s*(\S+)\s*$', line)
        if not m or line.lstrip().startswith("#"):
            continue
        if m.group(1):
            entries.append({})
        if entries:
            entries[-1][m.group(2)] = m.group(3)
    for e in entries:
        if not all(k in e for k in ("name", "source", "version", "repository", "commit")):
            continue
        # An entry with required_by is a dependency of another vendored
        # module that the vendoring script discovered by itself.
        comment = f"Vendored in vendors/{e['name']} from {e['repository']} at commit {e['commit']}"
        if "required_by" in e:
            comment += f"; automatically discovered dependency of vendors/{e['required_by']}"
        add_dependency(
            doc, root_id, f"SPDXRef-vendored-{safe_spdx_id(e['name'])}", e["source"], e["version"],
            download_location=f"git+{e['repository']}@{e['commit']}",
            package_type="LIBRARY",
            purl=f"pkg:terraform/{e['source']}@{e['version']}",
            comment=comment,
            relationship="CONTAINS"
        )


def generate_agent_sbom(entries_path, version, sbom_dir):
    # entries_path: JSONL file, one module record per line, written by the
    # helm/tofu publish steps. Each module becomes a CONTAINS dependency,
    # referenced by an oci purl per registry with the package digest as checksum.
    # The module's own SBOM, what the module is made of, is referenced as an
    # external document: by its namespace and by the checksum of the file,
    # which is also the digest of the layer of the SBOM attached to the package.
    doc = make_document("index", version, "index")
    doc["externalDocumentRefs"] = []

    root_id  = "SPDXRef-index-root"
    repo_url = f"{REGISTRY}/index"
    add_root_package(
        doc, root_id, "index", version,
        download_location=f"oci://{repo_url}",
        package_type="LIBRARY",
        # Digest not available for the agent package itself at generation time
        purl=f"pkg:oci/index?repository_url={repo_url}&tag={version}"
    )

    with open(entries_path) as f:
        entries = [json.loads(line) for line in f if line.strip()]

    for e in entries:
        spdx_id = f"SPDXRef-module-{safe_spdx_id(e['type'])}-{safe_spdx_id(e['name'])}"

        # One oci purl per registry — same artifact, different manifest digest
        ext_refs = []
        checksums = []
        # The entry's registries are ordered: primary first, then mirrors.
        for reg, r in e["registries"].items():
            ext_refs.append({
                "referenceCategory": "PACKAGE-MANAGER",
                "referenceType": "purl",
                # Digest is the version component; tag kept as a qualifier
                "referenceLocator": f"pkg:oci/{e['name'].lower()}@{r['package']}"
                                    f"?repository_url={r['repository']}&tag={e['tag']}"
            })
            # The package digest, once: the mirrors hold the same bytes
            checksum = {
                "algorithm": "SHA256",
                "checksumValue": r["package"].split(":", 1)[1]  # strip "sha256:"
            }
            if checksum not in checksums:
                checksums.append(checksum)

        sbom_name = e["name"] if e["type"] == "k8s" else f"{e['type']}-{e['name']}"
        with open(f"{sbom_dir}/{sbom_name}-sbom.spdx.json", "rb") as f:
            sbom = f.read()
        doc["externalDocumentRefs"].append({
            "externalDocumentId": f"DocumentRef-module-{safe_spdx_id(e['type'])}-{safe_spdx_id(e['name'])}",
            "spdxDocument": json.loads(sbom)["documentNamespace"],
            "checksum": {"algorithm": "SHA256", "checksumValue": hashlib.sha256(sbom).hexdigest()}
        })

        doc["packages"].append({
            "name": f"{e['type']}/{e['name']}",
            "SPDXID": spdx_id,
            "versionInfo": e["tag"],
            "supplier": f"Organization: {SUPPLIER}",
            "downloadLocation": f"oci://{next(iter(e['registries'].values()))['repository']}",
            "filesAnalyzed": False,
            "licenseConcluded": "NOASSERTION",
            "licenseDeclared": "NOASSERTION",
            "copyrightText": "NOASSERTION",
            "primaryPackagePurpose": "LIBRARY",
            "checksums": checksums,
            "externalRefs": ext_refs
        })
        doc["relationships"].append({
            "spdxElementId": root_id,
            "relatedSpdxElement": spdx_id,
            "relationshipType": "CONTAINS"
        })

    # The release's own container images, the "images" of manifest.json:
    # the digest once (a mirror holds the same image), a purl per registry.
    for image in json.loads(os.environ.get("SBOM_IMAGES") or "[]"):
        spdx_id = f"SPDXRef-image-{safe_spdx_id(image['name'])}"
        registries = list(image["registries"].values())
        doc["packages"].append({
            "name": image["name"],
            "SPDXID": spdx_id,
            "versionInfo": image["tag"],
            "supplier": f"Organization: {SUPPLIER}",
            "downloadLocation": f"oci://{registries[0]['repository']}",
            "filesAnalyzed": False,
            "licenseConcluded": "NOASSERTION",
            "licenseDeclared": "NOASSERTION",
            "copyrightText": "NOASSERTION",
            "primaryPackagePurpose": "CONTAINER",
            "checksums": [{"algorithm": "SHA256", "checksumValue": d.split(":", 1)[1]}
                          for d in sorted({r["digest"] for r in registries})],
            "externalRefs": [{
                "referenceCategory": "PACKAGE-MANAGER",
                "referenceType": "purl",
                "referenceLocator": f"pkg:oci/{image['name']}@{r['digest']}"
                                    f"?repository_url={r['repository']}&tag={image['tag']}"
            } for r in registries]
        })
        doc["relationships"].append({
            "spdxElementId": root_id,
            "relatedSpdxElement": spdx_id,
            "relationshipType": "CONTAINS"
        })

    return doc

def main():
    if len(sys.argv) < 4:
        print(f"Usage: {sys.argv[0]} helm <chart_dir> <chart_name> <version>", file=sys.stderr)
        print(f"       {sys.argv[0]} tofu <module_dir> <module_name> <version> <module_type>", file=sys.stderr)
        print(f"       {sys.argv[0]} agent <entries_jsonl> <version> <sbom_dir>", file=sys.stderr)
        sys.exit(1)

    mode = sys.argv[1]

    if mode == "helm":
        chart_dir, chart_name, version = sys.argv[2], sys.argv[3], sys.argv[4]
        doc = generate_helm_sbom(chart_dir, chart_name, version)
    elif mode == "tofu":
        module_dir, module_name, version, module_type = sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5]
        doc = generate_tofu_sbom(module_dir, module_name, version, module_type)
    elif mode == "agent":
        entries_path, version, sbom_dir = sys.argv[2], sys.argv[3], sys.argv[4]
        doc = generate_agent_sbom(entries_path, version, sbom_dir)
    else:
        print(f"Unknown mode: {mode}", file=sys.stderr)
        sys.exit(1)

    print(json.dumps(doc, indent=2))


if __name__ == "__main__":
    main()
