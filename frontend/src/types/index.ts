export interface Task {
  id: string;
  content: string;
  rules: {
    minimum_agreement: number;
    match_strategy: string;
    numeric_tolerance: number;
    category: string;
  };
  constraints: {
    worker_count: number;
    timeout: string;
    priority: number;
    retry_attempts: number;
    require_all_pass: boolean;
  };
  status: string;
  created_at: string;
}

export interface Worker {
  id: string;
  status: string;
  current_task_id?: string;
  voting_power: number;
  last_heartbeat: string;
}

export interface WorkerNode {
  id: string;
  group: number;
  label: string;
}

export interface WorkerLink {
  source: string;
  target: string;
  value: number;
}
