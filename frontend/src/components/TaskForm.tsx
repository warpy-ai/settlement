import React, { useState } from 'react';
import {
  Box,
  Button,
  TextField,
  Select,
  MenuItem,
  FormControl,
  InputLabel,
  Card,
  CardContent,
  Typography,
} from '@mui/material';
import { api } from '../api';

export const TaskForm: React.FC = () => {
  const [formData, setFormData] = useState({
    content: '',
    rules: {
      minimum_agreement: 0.8,
      match_strategy: 'exact_match',
      numeric_tolerance: 0.1,
      category: 'moderation'
    },
    constraints: {
      worker_count: 5,
      timeout: '1m',
      priority: 1,
      retry_attempts: 2,
      require_all_pass: true
    }
  });

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await api.createTask(formData);
      // Reset form or show success message
      alert('Task created successfully!');
    } catch (error) {
      console.error('Error creating task:', error);
      alert('Error creating task. Please try again.');
    }
  };

  return (
    <Card>
      <CardContent>
        <Typography variant="h5" gutterBottom>
          Create New Task
        </Typography>
        <Box component="form" onSubmit={handleSubmit} sx={{ mt: 2 }}>
          <TextField
            fullWidth
            multiline
            rows={4}
            label="Task Content"
            value={formData.content}
            onChange={(e) => setFormData({
              ...formData,
              content: e.target.value
            })}
            margin="normal"
          />
          
          <FormControl fullWidth margin="normal">
            <InputLabel>Match Strategy</InputLabel>
            <Select
              value={formData.rules.match_strategy}
              onChange={(e) => setFormData({
                ...formData,
                rules: {
                  ...formData.rules,
                  match_strategy: e.target.value
                }
              })}
            >
              <MenuItem value="exact_match">Exact Match</MenuItem>
              <MenuItem value="semantic_match">Semantic Match</MenuItem>
            </Select>
          </FormControl>

          <TextField
            type="number"
            label="Worker Count"
            value={formData.constraints.worker_count}
            onChange={(e) => setFormData({
              ...formData,
              constraints: {
                ...formData.constraints,
                worker_count: parseInt(e.target.value)
              }
            })}
            margin="normal"
            inputProps={{ min: 1, step: 2 }}
          />

          <Box sx={{ mt: 2 }}>
            <Button
              type="submit"
              variant="contained"
              color="primary"
              fullWidth
            >
              Create Task
            </Button>
          </Box>
        </Box>
      </CardContent>
    </Card>
  );
};
