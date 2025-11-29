import React, { useCallback } from 'react';
import ForceGraph2D from 'react-force-graph-2d';
import { Worker, WorkerNode, WorkerLink } from '../types';

interface Props {
  workers: Worker[];
  tasks: { [key: string]: string }; // taskId -> content mapping
}

export const WorkerGraph: React.FC<Props> = ({ workers, tasks }) => {
  const prepareGraphData = useCallback(() => {
    const nodes: WorkerNode[] = workers.map(worker => ({
      id: worker.id,
      group: worker.status === 'busy' ? 1 : 0,
      label: `Worker ${worker.id}\n${worker.status}`
    }));

    const links: WorkerLink[] = workers
      .filter(worker => worker.current_task_id)
      .map(worker => ({
        source: worker.id,
        target: worker.current_task_id!,
        value: 1
      }));

    // Add task nodes
    const taskNodes = [...new Set(workers
      .filter(w => w.current_task_id)
      .map(w => w.current_task_id))]
      .map(taskId => ({
        id: taskId!,
        group: 2,
        label: `Task ${taskId}\n${tasks[taskId!]?.substring(0, 20) || ''}`
      }));

    return {
      nodes: [...nodes, ...taskNodes],
      links
    };
  }, [workers, tasks]);

  const graphData = prepareGraphData();

  return (
    <div style={{ width: '100%', height: '600px', border: '1px solid #ddd' }}>
      <ForceGraph2D
        graphData={graphData}
        nodeLabel="label"
        nodeColor={node => 
          node.group === 0 ? '#4CAF50' :  // available
          node.group === 1 ? '#FFA726' :  // busy
          '#2196F3'                       // task
        }
        nodeRelSize={6}
        linkColor={() => '#999'}
        linkWidth={1}
        linkDirectionalParticles={2}
      />
    </div>
  );
};
