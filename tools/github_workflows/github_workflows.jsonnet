local workflows_template = import 'tools/github_workflows/workflows_template.libsonnet';


workflows_template.getWorkflows(
  [
    'bonanza_bazel',
    'bonanza_browser',
    'bonanza_builder',
    'bonanza_fetcher',
    'bonanza_scheduler',
    'bonanza_storage_frontend',
    'bonanza_storage_shard',
    'bonanza_worker',
  ],
  [
    'bonanza_browser:bonanza_browser',
    'bonanza_builder:bonanza_builder',
    'bonanza_fetcher:bonanza_fetcher',
    'bonanza_scheduler:bonanza_scheduler',
    'bonanza_storage_frontend:bonanza_storage_frontend',
    'bonanza_storage_shard:bonanza_storage_shard',
    'bonanza_worker:bonanza_worker',
  ],
)
