import os
import json
import subprocess
import shutil
import tempfile
import argparse

def run_command(command, cwd=None):
    print(f"Running: {' '.join(command)}")
    result = subprocess.run(command, cwd=cwd, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"Error: {result.stderr}")
        return False
    return True

def main():
    parser = argparse.ArgumentParser(description="Bulk package Frappe apps and store them.")
    parser.add_argument("--apps", default="scripts/apps.json", help="Path to apps.json")
    parser.add_argument("--output", default="dist", help="Directory to store .fpm files")
    parser.add_argument("--repo", help="FPM repository to publish to")
    parser.add_argument("--fpm-bin", default="./fpm-cli", help="Path to fpm binary")

    args = parser.parse_args()

    if not os.path.exists(args.apps):
        print(f"Error: {args.apps} not found.")
        return

    if not os.path.exists(args.output):
        os.makedirs(args.output)

    with open(args.apps, 'r') as f:
        apps = json.load(f)

    with tempfile.TemporaryDirectory() as tmp_dir:
        for app in apps:
            app_name = app['name']
            app_url = app['url']
            app_version = app['version']
            app_org = app['org']

            print(f"\n--- Processing {app_name} ({app_version}) ---")

            # 1. Clone App
            app_dir = os.path.join(tmp_dir, app_name)
            if not run_command(["git", "clone", "--depth", "1", app_url, app_dir]):
                print(f"Failed to clone {app_name}. Skipping...")
                continue

            # 2. Package App
            if not run_command([
                args.fpm_bin, "package", 
                "--version", app_version, 
                "--org", app_org,
                "--output-path", os.path.abspath(args.output),
                "--overwrite"
            ], cwd=app_dir):
                print(f"Failed to package {app_name}. Skipping...")
                continue

            # 3. Publish App (Optional)
            if args.repo:
                package_file = os.path.abspath(os.path.join(args.output, f"{app_name}-{app_version}.fpm"))
                if not run_command([
                    args.fpm_bin, "publish",
                    "--from-file", package_file,
                    "--repo", args.repo
                ]):
                    print(f"Failed to publish {app_name}. Skipping...")
                    continue

    print("\nBulk packaging complete!")

if __name__ == "__main__":
    main()
