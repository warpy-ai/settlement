import React from 'react';
import {
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Paper,
  Chip,
} from '@mui/material';
import { Task } from '../types';

interface Props {
  tasks: Task[];
}

export const TaskList: React.FC<Props> = ({ tasks }) => {
  return (
    <TableContainer component={Paper}>
      <Table>
        <TableHead>
          <TableRow>
            <TableCell>ID</TableCell>
            <TableCell>Content</TableCell>
            <TableCell>Category</TableCell>
            <TableCell>Workers</TableCell>
            <TableCell>Status</TableCell>
            <TableCell>Created At</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {tasks.map((task) => (
            <TableRow key={task.id}>
              <TableCell>{task.id}</TableCell>
              <TableCell>
                {task.content.length > 50
                  ? `${task.content.substring(0, 50)}...`
                  : task.content}
              </TableCell>
              <TableCell>{task.rules.category}</TableCell>
              <TableCell>{task.constraints.worker_count}</TableCell>
              <TableCell>
                <Chip
                  label={task.status}
                  color={
                    task.status === 'completed'
                      ? 'success'
                      : task.status === 'failed'
                      ? 'error'
                      : 'warning'
                  }
                  size="small"
                />
              </TableCell>
              <TableCell>
                {new Date(task.created_at).toLocaleString()}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
};
