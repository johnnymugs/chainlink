import reducer, { INITIAL_STATE } from './reducers'
import { setAnswers } from './actions'

describe('state/ducks/listing/reducers', () => {
  describe('SET_ANSWERS', () => {
    it('should replace answers', () => {
      const data = [
        {
          answer: 'answer',
        },
      ]
      const action = setAnswers(data)
      const state = reducer(INITIAL_STATE, action)

      expect(state.answers).toEqual(data)
    })
  })
})
